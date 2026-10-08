package golf

// Field operations (Rhapsody parity, roadmap §13): check-in, readiness,
// starter queue and control, caddy, golf cart, locker, bag drop and bag
// storage, marshal course & weather status.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	"oneclub/internal/membership"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/notification"
	"oneclub/internal/platform/notify"
)

func membershipAccountHolder(ctx context.Context, q dbtx.Querier, memberID uuid.UUID) (*uuid.UUID, error) {
	return membership.AccountHolder(ctx, q, memberID)
}

func customerExists(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID) (bool, error) {
	return crm.ExistsInProperty(ctx, q, property, customerID)
}

// ── check-in (FR-CHK-01/02) ───────────────────────────────────────────────

// CheckInRequest identifies who arrives.
type CheckInRequest struct {
	Method    string      `json:"method" enum:"member_card,booking_qr,booking_code,name"`
	Value     string      `json:"value,omitempty" doc:"Scanned card / QR, booking code or name"`
	BookingID *uuid.UUID  `json:"bookingId,omitempty"`
	PlayerIDs []uuid.UUID `json:"playerIds,omitempty" doc:"Players to check in (default: the card holder, or the whole booking)"`
	Date      string      `json:"date,omitempty" doc:"Play date (default today)"`
}

// CheckInCandidate is a booking found by a lookup.
type CheckInCandidate struct {
	BookingID   uuid.UUID   `json:"bookingId"`
	Code        string      `json:"code"`
	StartAt     time.Time   `json:"startAt"`
	LocalTime   string      `json:"localTime"`
	ContactName string      `json:"contactName"`
	Status      string      `json:"status"`
	Players     []Player    `json:"players"`
	MatchedIDs  []uuid.UUID `json:"matchedPlayerIds"`
	PaymentDue  string      `json:"paymentDue" doc:"Amount to pay before check-in (0 = none)"`
}

// CheckInResult reports a check-in.
type CheckInResult struct {
	Booking      Booking     `json:"booking"`
	CheckedIn    []uuid.UUID `json:"checkedIn"`
	FlightsReady []uuid.UUID `json:"flightsReady"`
}

// ErrPaymentRequired blocks check-in until the folio is paid (FR-CHK-02).
func errPaymentRequired(amount string) error {
	return errs.Conflict("payment_required", "payment of "+amount+" is required before check-in (Payment Policy)")
}

// findForCheckIn resolves the bookings of today matching the scan.
func (m *Module) findForCheckIn(ctx context.Context, tx pgx.Tx, property uuid.UUID, req CheckInRequest) ([]CheckInCandidate, error) {
	loc := location(ctx, tx, property)
	day := localDay(clock.Now(), loc)
	if req.Date != "" {
		d, err := parseDate(req.Date)
		if err != nil {
			return nil, err
		}
		day = d
	}
	value := strings.TrimSpace(req.Value)
	var where string
	var args []any
	var memberID *uuid.UUID
	switch req.Method {
	case "booking_qr":
		where, args = "b.qr_token = $2", []any{property, strings.TrimPrefix(value, "oneclub:booking:")}
	case "booking_code":
		where, args = "upper(b.code) = upper($2)", []any{property, value}
	case "member_card":
		mid, err := membership.CardLookup(ctx, tx, property, value)
		if err != nil {
			return nil, err
		}
		if mid == nil {
			return nil, errs.NotFound("member card")
		}
		memberID = mid
		where, args = "(b.member_id = $2 OR EXISTS (SELECT 1 FROM golf.booking_players x WHERE x.booking_id = b.id AND x.member_id = $2)) AND b.play_date = $3::date",
			[]any{property, *mid, day.Format("2006-01-02")}
	case "name":
		if len(value) < 2 {
			return nil, errs.Validation("name_too_short", "type at least 2 letters", errs.Field("value", "invalid", "≥ 2 letters"))
		}
		where, args = "(b.contact_name ILIKE $2 OR EXISTS (SELECT 1 FROM golf.booking_players x WHERE x.booking_id = b.id AND x.name ILIKE $2)) AND b.play_date = $3::date",
			[]any{property, "%" + value + "%", day.Format("2006-01-02")}
	default:
		if req.BookingID != nil {
			where, args = "b.id = $2", []any{property, *req.BookingID}
		} else {
			return nil, errs.Validation("invalid_method", "method must be member_card, booking_qr, booking_code or name", errs.Field("method", "invalid", "check-in method"))
		}
	}
	rows, err := tx.Query(ctx, `SELECT b.id FROM golf.bookings b WHERE b.property_id = $1 AND `+where+`
		AND b.status IN ('confirmed', 'checked_in', 'pending') ORDER BY b.start_at LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var x uuid.UUID
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, x)
	}
	rows.Close()
	out := []CheckInCandidate{}
	for _, bid := range ids {
		b, err := GetBooking(ctx, tx, bid)
		if err != nil {
			return nil, err
		}
		c := CheckInCandidate{BookingID: b.ID, Code: b.Code, StartAt: b.StartAt, LocalTime: b.LocalTime, ContactName: b.ContactName, Status: b.Status,
			Players: b.Players, MatchedIDs: []uuid.UUID{}, PaymentDue: "0"}
		for _, p := range b.Players {
			if memberID != nil && p.MemberID != nil && *p.MemberID == *memberID {
				c.MatchedIDs = append(c.MatchedIDs, p.ID)
			}
			if req.Method == "name" && strings.Contains(strings.ToLower(p.Name), strings.ToLower(value)) {
				c.MatchedIDs = append(c.MatchedIDs, p.ID)
			}
		}
		if due, err := m.paymentDue(ctx, tx, property, b); err == nil {
			c.PaymentDue = due.StringFixed(0)
		}
		out = append(out, c)
	}
	return out, nil
}

// paymentDue returns what must be paid before check-in under the policy.
func (m *Module) paymentDue(ctx context.Context, tx pgx.Tx, property uuid.UUID, b Booking) (decimal.Decimal, error) {
	if b.FolioID == nil {
		return decimal.Zero, nil
	}
	pol, err := LoadPoliciesAsOf(ctx, tx, property, b.CreatedAt)
	if err != nil {
		return decimal.Zero, err
	}
	sum, err := billing.FolioSummary(ctx, tx, *b.FolioID)
	if err != nil {
		return decimal.Zero, err
	}
	bal := dec(sum.Balance)
	if !bal.IsPositive() || !pol.Payment.PayBeforeCheckIn {
		return decimal.Zero, nil
	}
	if b.PaymentMode != nil && *b.PaymentMode == "deposit" && dec(sum.HeldDeposits).IsPositive() {
		return decimal.Zero, nil
	}
	return bal, nil
}

// CheckIn checks players in (online or from the offline sync queue).
func (m *Module) CheckIn(ctx context.Context, tx pgx.Tx, property uuid.UUID, req CheckInRequest, method string) (CheckInResult, error) {
	var out CheckInResult
	cands, err := m.findForCheckIn(ctx, tx, property, req)
	if err != nil {
		return out, err
	}
	if len(cands) == 0 {
		return out, errs.NotFound("booking for today")
	}
	var cand CheckInCandidate
	if req.BookingID != nil {
		found := false
		for _, c := range cands {
			if c.BookingID == *req.BookingID {
				cand, found = c, true
			}
		}
		if !found {
			return out, errs.NotFound("booking")
		}
	} else if len(cands) > 1 && len(req.PlayerIDs) == 0 {
		return out, errs.Conflict("multiple_bookings", "several bookings match; choose one (lookup first)")
	} else {
		cand = cands[0]
		if len(req.PlayerIDs) > 0 {
			for _, c := range cands {
				for _, p := range c.Players {
					if p.ID == req.PlayerIDs[0] {
						cand = c
					}
				}
			}
		}
	}
	b, err := lockBooking(ctx, tx, property, cand.BookingID)
	if err != nil {
		return out, err
	}
	if b.Status != "confirmed" && b.Status != "checked_in" {
		return out, errs.Conflict("booking_not_confirmed", "the booking is "+b.Status+"; confirm or pay it first")
	}
	if due, err := m.paymentDue(ctx, tx, property, b); err != nil {
		return out, err
	} else if due.IsPositive() {
		return out, errPaymentRequired("IDR " + due.StringFixed(0))
	}
	targets := req.PlayerIDs
	if len(targets) == 0 {
		targets = cand.MatchedIDs
	}
	if len(targets) == 0 {
		for _, p := range b.Players {
			targets = append(targets, p.ID)
		}
	}
	flights := map[uuid.UUID]bool{}
	for _, tid := range targets {
		var p *Player
		for i := range b.Players {
			if b.Players[i].ID == tid {
				p = &b.Players[i]
			}
		}
		if p == nil {
			return out, errs.Validation("invalid_player", "player not in this booking", errs.Field("playerIds", "invalid", "player of this booking"))
		}
		if p.Status == "checked_in" {
			continue
		}
		if p.Status != "booked" {
			continue
		}
		if p.TBA {
			return out, errs.Conflict("player_tba", "complete the details of player "+p.Name+" before check-in")
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET status = 'checked_in', checked_in_at = now(), check_in_method = $2 WHERE id = $1`, tid, method); err != nil {
			return out, err
		}
		out.CheckedIn = append(out.CheckedIn, tid)
		flights[p.FlightID] = true
		if _, err := m.Events.Publish(ctx, tx, EventPlayerCheckedIn, "golf.booking_player", &tid, &property, map[string]any{"bookingId": b.ID,
			"playerId": tid, "flightId": p.FlightID, "method": method, "packageBookingId": b.PackageBookingID,
			"packageComponentId": b.PackageComponentID, "code": b.Code}); err != nil {
			return out, err
		}
	}
	if out.CheckedIn == nil {
		out.CheckedIn = []uuid.UUID{}
	}
	if len(out.CheckedIn) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET status = 'checked_in', checked_in_at = coalesce(checked_in_at, now()) WHERE id = $1`, b.ID); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'checked_in' WHERE id = ANY($1) AND status = 'confirmed'`, keys(flights)); err != nil {
			return out, err
		}
		if err := addHistory(ctx, tx, property, b.ID, "checked_in", nil, map[string]any{"players": out.CheckedIn, "method": method}, ""); err != nil {
			return out, err
		}
	}
	out.FlightsReady = []uuid.UUID{}
	for fid := range flights {
		ready, err := m.EvaluateReadiness(ctx, tx, property, fid)
		if err != nil {
			return out, err
		}
		if ready {
			out.FlightsReady = append(out.FlightsReady, fid)
		}
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "checked_in", b.CourseID, mustDay(b.PlayDate), map[string]any{"bookingId": b.ID.String()}); err != nil {
		return out, err
	}
	out.Booking, err = GetBooking(ctx, tx, b.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "check_in", EntityType: "golf.booking", EntityID: b.ID.String(), EntityLabel: b.Code,
		PropertyID: &property, After: map[string]any{"players": out.CheckedIn, "method": method}})
}

func keys(m map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// EvaluateReadiness marks a flight Ready (FR-CHK-06) and puts it in the
// starter queue.
func (m *Module) EvaluateReadiness(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID) (bool, error) {
	pol, err := LoadPolicies(ctx, tx, property, clock.Now())
	if err != nil {
		return false, err
	}
	flights, err := m.loadFlights(ctx, tx, pol, "f.id = $1", fid)
	if err != nil || len(flights) == 0 {
		return false, err
	}
	f := flights[0]
	if !f.Readiness.Ready || (f.Status != "checked_in" && f.Status != "confirmed") {
		return f.Status == "ready", nil
	}
	var course uuid.UUID
	var day, start time.Time
	if err := tx.QueryRow(ctx, `UPDATE golf.flights SET status = 'ready', ready_at = now() WHERE id = $1 RETURNING course_id, play_date,
		(SELECT start_at FROM golf.tee_times WHERE id = golf.flights.tee_time_id)`, fid).Scan(&course, &day, &start); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.starter_queue (flight_id, property_id, course_id, play_date, scheduled_at, ready_at, position, status)
		VALUES ($1,$2,$3,$4,$5,now(),$6,'waiting') ON CONFLICT (flight_id) DO UPDATE SET ready_at = now(), status = 'waiting'`,
		fid, property, course, day.Format("2006-01-02"), start, start.Unix()); err != nil {
		return false, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventFlightReady, "golf.flight", &fid, &property, map[string]any{"flightId": fid, "courseId": course}); err != nil {
		return false, err
	}
	return true, notifyRealtime(ctx, tx, property, "golf.starter_queue", "flight_ready", course, day, map[string]any{"flightId": fid.String()})
}

// ── starter queue & control (FR-CHK-07..10) ───────────────────────────────

// QueueEntry is one flight in the starter queue.
type QueueEntry struct {
	FlightID     uuid.UUID     `json:"flightId"`
	Position     int           `json:"position" doc:"1-based position in the Active Dispatch Queue (0 = on hold)"`
	Status       string        `json:"status" enum:"waiting,on_hold,dispatched,removed"`
	ScheduledAt  time.Time     `json:"scheduledAt"`
	LocalTime    string        `json:"localTime"`
	StartTee     int           `json:"startTee"`
	ReadyAt      time.Time     `json:"readyAt"`
	HoldReason   *string       `json:"holdReason"`
	CalledAt     *time.Time    `json:"calledAt"`
	DispatchedAt *time.Time    `json:"dispatchedAt"`
	Skips        int           `json:"skips"`
	BookingCode  *string       `json:"bookingCode"`
	Players      []SheetPlayer `json:"players"`
	GolfCarts    []string      `json:"golfCarts"`
	WaitMinutes  int           `json:"waitMinutes"`
}

// StarterQueue is the queue of a course and day.
type StarterQueue struct {
	CourseID     uuid.UUID    `json:"courseId"`
	Date         string       `json:"date"`
	Active       []QueueEntry `json:"active" doc:"Active Dispatch Queue in order"`
	OnHold       []QueueEntry `json:"onHold" doc:"Held flights keep their Preserved Queue Position"`
	Dispatched   []QueueEntry `json:"dispatched"`
	CourseStatus CourseStatus `json:"courseStatus"`
}

// LoadQueue builds the starter queue view.
func (m *Module) LoadQueue(ctx context.Context, q dbtx.Querier, property, courseID uuid.UUID, day time.Time) (StarterQueue, error) {
	out := StarterQueue{CourseID: courseID, Date: day.Format("2006-01-02"), Active: []QueueEntry{}, OnHold: []QueueEntry{}, Dispatched: []QueueEntry{}}
	loc := location(ctx, q, property)
	rows, err := q.Query(ctx, `SELECT sq.flight_id, sq.status, sq.scheduled_at, t.start_tee, sq.ready_at, sq.hold_reason, sq.called_at, sq.dispatched_at, sq.skips, b.code
		FROM golf.starter_queue sq JOIN golf.flights f ON f.id = sq.flight_id JOIN golf.tee_times t ON t.id = f.tee_time_id LEFT JOIN golf.bookings b ON b.id = f.booking_id
		WHERE sq.property_id = $1 AND sq.course_id = $2 AND sq.play_date = $3::date AND sq.status <> 'removed'
		ORDER BY sq.position, sq.ready_at, sq.flight_id`, property, courseID, day.Format("2006-01-02"))
	if err != nil {
		return out, err
	}
	var entries []QueueEntry
	for rows.Next() {
		var e QueueEntry
		if err := rows.Scan(&e.FlightID, &e.Status, &e.ScheduledAt, &e.StartTee, &e.ReadyAt, &e.HoldReason, &e.CalledAt, &e.DispatchedAt, &e.Skips, &e.BookingCode); err != nil {
			rows.Close()
			return out, err
		}
		e.LocalTime = e.ScheduledAt.In(loc).Format("15:04")
		end := clock.Now()
		if e.DispatchedAt != nil {
			end = *e.DispatchedAt
		}
		e.WaitMinutes = int(end.Sub(e.ReadyAt).Minutes())
		entries = append(entries, e)
	}
	rows.Close()
	if len(entries) > 0 {
		ids := make([]uuid.UUID, len(entries))
		for i := range entries {
			ids[i] = entries[i].FlightID
		}
		pol, err := LoadPolicies(ctx, q, property, clock.Now())
		if err != nil {
			return out, err
		}
		fl, err := m.loadFlights(ctx, q, pol, "f.id = ANY($1)", ids)
		if err != nil {
			return out, err
		}
		byID := map[uuid.UUID]flightRow{}
		for _, f := range fl {
			byID[f.ID] = f
		}
		pos := 0
		for _, e := range entries {
			f := byID[e.FlightID]
			e.Players, e.GolfCarts = f.Players, f.Carts
			switch e.Status {
			case "waiting":
				pos++
				e.Position = pos
				out.Active = append(out.Active, e)
			case "on_hold":
				out.OnHold = append(out.OnHold, e)
			case "dispatched":
				out.Dispatched = append(out.Dispatched, e)
			}
		}
	}
	out.CourseStatus, err = loadCourseStatus(ctx, q, courseID)
	return out, err
}

// StarterAction is a starter control request.
type StarterAction struct {
	Reason      string `json:"reason,omitempty" doc:"Required for Hold (operational reason)"`
	HolesPlayed *int   `json:"holesPlayed,omitempty" doc:"Finish: holes actually played"`
}

func (m *Module) queueEntry(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID) (uuid.UUID, time.Time, string, float64, error) {
	var course uuid.UUID
	var day time.Time
	var status string
	var position float64
	err := tx.QueryRow(ctx, `SELECT course_id, play_date, status, position FROM golf.starter_queue WHERE flight_id = $1 AND property_id = $2 FOR UPDATE`,
		fid, property).Scan(&course, &day, &status, &position)
	if dbtx.IsNoRows(err) {
		return course, day, status, position, errs.NotFound("flight in the starter queue")
	}
	return course, day, status, position, err
}

// Control runs Hold, Skip +1, Release, Call, Tee-Off or Finish.
func (m *Module) Control(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID, action string, req StarterAction) (StarterQueue, error) {
	var out StarterQueue
	if action == "finish" {
		var course uuid.UUID
		var day time.Time
		if err := tx.QueryRow(ctx, `SELECT course_id, play_date FROM golf.flights WHERE id = $1 AND property_id = $2`, fid, property).Scan(&course, &day); err != nil {
			return out, errs.NotFound("flight")
		}
		if err := m.finishFlight(ctx, tx, property, fid, req.HolesPlayed); err != nil {
			return out, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "round_finish", EntityType: "golf.flight", EntityID: fid.String(), PropertyID: &property,
			After: map[string]any{"holesPlayed": req.HolesPlayed}}); err != nil {
			return out, err
		}
		return m.LoadQueue(ctx, tx, property, course, day)
	}
	course, day, status, position, err := m.queueEntry(ctx, tx, property, fid)
	if err != nil {
		return out, err
	}
	switch action {
	case "hold":
		if strings.TrimSpace(req.Reason) == "" {
			return out, errs.Validation("reason_required", "an operational reason is required to hold a flight", errs.Field("reason", "required", "reason"))
		}
		if status != "waiting" {
			return out, errs.Conflict("not_waiting", "only waiting flights can be held")
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.starter_queue SET status = 'on_hold', hold_reason = $2, held_at = now() WHERE flight_id = $1`, fid, req.Reason); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'on_hold' WHERE id = $1`, fid); err != nil {
			return out, err
		}
	case "release":
		if status != "on_hold" {
			return out, errs.Conflict("not_on_hold", "only held flights can be released")
		}
		// the preserved position value puts it straight back in its place
		if _, err := tx.Exec(ctx, `UPDATE golf.starter_queue SET status = 'waiting', released_at = now() WHERE flight_id = $1`, fid); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'ready' WHERE id = $1`, fid); err != nil {
			return out, err
		}
	case "skip":
		if status != "waiting" {
			return out, errs.Conflict("not_waiting", "only waiting flights can be skipped")
		}
		var next []float64
		rows, err := tx.Query(ctx, `SELECT position FROM golf.starter_queue WHERE course_id = $1 AND play_date = $2 AND status = 'waiting' AND position > $3
			ORDER BY position LIMIT 2`, course, day, position)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var p float64
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return out, err
			}
			next = append(next, p)
		}
		rows.Close()
		if len(next) == 0 {
			return out, errs.Conflict("last_in_queue", "the flight is already last in the queue")
		}
		np := next[0] + 0.5
		if len(next) == 2 {
			np = (next[0] + next[1]) / 2
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.starter_queue SET position = $2, skips = skips + 1 WHERE flight_id = $1`, fid, np); err != nil {
			return out, err
		}
	case "call":
		if _, err := tx.Exec(ctx, `UPDATE golf.starter_queue SET called_at = now() WHERE flight_id = $1`, fid); err != nil {
			return out, err
		}
	case "tee-off":
		if status != "waiting" {
			return out, errs.Conflict("not_waiting", "release the flight before tee-off")
		}
		cs, err := loadCourseStatus(ctx, tx, course)
		if err != nil {
			return out, err
		}
		if cs.CourseState == "closed" || cs.Weather == "rain_stop" || cs.Weather == "lightning_warning" {
			return out, errs.Conflict("course_not_playable", "tee-off is suspended: course "+cs.CourseState+", weather "+strings.ReplaceAll(cs.Weather, "_", " "))
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.starter_queue SET status = 'dispatched', dispatched_at = now() WHERE flight_id = $1`, fid); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'in_play', tee_off_at = now() WHERE id = $1`, fid); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET status = 'in_play', started_at = now() WHERE flight_id = $1 AND status = 'assigned'`, fid); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.golf_cart_assignments SET status = 'in_use', out_at = now() WHERE flight_id = $1 AND status = 'assigned'`, fid); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.golf_carts SET readiness = 'in_use', readiness_changed_at = now() WHERE id IN
			(SELECT golf_cart_id FROM golf.golf_cart_assignments WHERE flight_id = $1 AND status = 'in_use')`, fid); err != nil {
			return out, err
		}
		if _, err := m.Events.Publish(ctx, tx, EventFlightTeedOff, "golf.flight", &fid, &property, map[string]any{"flightId": fid, "courseId": course}); err != nil {
			return out, err
		}
	default:
		return out, errs.BadRequest("invalid_action", "unknown starter action")
	}
	if err := notifyRealtime(ctx, tx, property, "golf.starter_queue", action, course, day, map[string]any{"flightId": fid.String()}); err != nil {
		return out, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "starter_"+action, course, day, map[string]any{"flightId": fid.String()}); err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "starter_" + strings.ReplaceAll(action, "-", "_"), EntityType: "golf.flight", EntityID: fid.String(),
		PropertyID: &property, Reason: req.Reason, Before: map[string]any{"status": status}}); err != nil {
		return out, err
	}
	return m.LoadQueue(ctx, tx, property, course, day)
}

// finishFlight records Round Finish and frees caddies, carts and lockers.
func (m *Module) finishFlight(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID, holes *int) error {
	var status string
	var bookingID *uuid.UUID
	var course uuid.UUID
	var day time.Time
	if err := tx.QueryRow(ctx, `SELECT status, booking_id, course_id, play_date FROM golf.flights WHERE id = $1 AND property_id = $2 FOR UPDATE`, fid, property).
		Scan(&status, &bookingID, &course, &day); err != nil {
		return errs.NotFound("flight")
	}
	if status != "in_play" {
		return errs.Conflict("not_in_play", "only flights In Play can finish")
	}
	if err := closePause(ctx, tx, fid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'completed', round_finish_at = now(), holes_played = $2 WHERE id = $1`, fid, holes); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET status = 'completed', finished_at = now(),
		period = tstzrange(lower(period), greatest(lower(period) + interval '1 minute', now()), '[)') WHERE flight_id = $1 AND status IN ('assigned', 'in_play')`, fid); err != nil {
		return err
	}
	pol, err := LoadPolicies(ctx, tx, property, clock.Now())
	if err != nil {
		return err
	}
	after := pol.Cart.AfterReturn
	if after != "charging" {
		after = "not_ready"
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.golf_carts SET readiness = CASE WHEN cart_type = 'electric' THEN $2 ELSE 'not_ready' END, readiness_changed_at = now()
		WHERE id IN (SELECT golf_cart_id FROM golf.golf_cart_assignments WHERE flight_id = $1 AND status IN ('assigned','in_use'))`, fid, after); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.golf_cart_assignments SET status = 'returned', returned_at = now(),
		period = tstzrange(lower(period), greatest(lower(period) + interval '1 minute', now()), '[)') WHERE flight_id = $1 AND status IN ('assigned', 'in_use')`, fid); err != nil {
		return err
	}
	// daily lockers stay in use after the round (shower, change): they return
	// to Available at the Golfer Check-out (FR-CHK-05, checkout.go)
	if bookingID != nil {
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM golf.flights WHERE booking_id = $1 AND status NOT IN ('completed', 'cancelled')`, *bookingID).Scan(&open); err != nil {
			return err
		}
		if open == 0 {
			if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET status = 'completed', completed_at = now() WHERE id = $1 AND status = 'checked_in'`, *bookingID); err != nil {
				return err
			}
			if err := addHistory(ctx, tx, property, *bookingID, "completed", nil, map[string]any{"status": "completed"}, ""); err != nil {
				return err
			}
		}
	}
	if _, err := m.Events.Publish(ctx, tx, EventRoundFinished, "golf.flight", &fid, &property, map[string]any{"flightId": fid, "holesPlayed": holes}); err != nil {
		return err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "round_finished", course, day, map[string]any{"flightId": fid.String()}); err != nil {
		return err
	}
	return notifyRealtime(ctx, tx, property, "golf.boards", "round_finished", course, day, nil)
}

// ── course & weather status (FR-CHK-11) ───────────────────────────────────

// CourseStatus is the marshal status of a course.
type CourseStatus struct {
	CourseID    uuid.UUID  `json:"courseId"`
	CourseName  string     `json:"courseName"`
	CourseState string     `json:"courseState" enum:"open,closed"`
	Weather     string     `json:"weather" enum:"normal,rain,lightning_warning,rain_stop,heat_warning"`
	Lighting    string     `json:"lighting" enum:"on,off"`
	ClosedHoles string     `json:"closedHoles"`
	Notes       *string    `json:"notes"`
	UpdatedAt   *time.Time `json:"updatedAt"`
}

func loadCourseStatus(ctx context.Context, q dbtx.Querier, courseID uuid.UUID) (CourseStatus, error) {
	cs := CourseStatus{CourseID: courseID, CourseState: "open", Weather: "normal", Lighting: "off"}
	err := q.QueryRow(ctx, `SELECT c.name, coalesce(s.course_state, 'open'), coalesce(s.weather, 'normal'), coalesce(s.lighting, 'off'), coalesce(s.closed_holes, ''),
		s.notes, s.updated_at FROM golf.courses c LEFT JOIN golf.course_status s ON s.course_id = c.id WHERE c.id = $1`, courseID).
		Scan(&cs.CourseName, &cs.CourseState, &cs.Weather, &cs.Lighting, &cs.ClosedHoles, &cs.Notes, &cs.UpdatedAt)
	if dbtx.IsNoRows(err) {
		return cs, errs.NotFound("course")
	}
	return cs, err
}

// CourseStatusRequest updates the marshal status.
type CourseStatusRequest struct {
	CourseState string  `json:"courseState,omitempty" enum:"open,closed"`
	Weather     string  `json:"weather,omitempty" enum:"normal,rain,lightning_warning,rain_stop,heat_warning"`
	Lighting    string  `json:"lighting,omitempty" enum:"on,off"`
	ClosedHoles *string `json:"closedHoles,omitempty" doc:"Comma separated hole numbers"`
	Notes       *string `json:"notes,omitempty"`
}

// UpdateCourseStatus sets course / weather / lights and notifies the staff.
func (m *Module) UpdateCourseStatus(ctx context.Context, tx pgx.Tx, property, courseID uuid.UUID, req CourseStatusRequest) (CourseStatus, error) {
	before, err := loadCourseStatus(ctx, tx, courseID)
	if err != nil {
		return before, err
	}
	var owner uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM golf.courses WHERE id = $1`, courseID).Scan(&owner); err != nil || owner != property {
		return before, errs.NotFound("course")
	}
	after := before
	if req.CourseState != "" {
		if req.CourseState != "open" && req.CourseState != "closed" {
			return before, errs.Validation("invalid_state", "course state must be open or closed", errs.Field("courseState", "invalid", "open or closed"))
		}
		after.CourseState = req.CourseState
	}
	if req.Weather != "" {
		switch req.Weather {
		case "normal", "rain", "lightning_warning", "rain_stop", "heat_warning":
		default:
			return before, errs.Validation("invalid_weather", "invalid weather status", errs.Field("weather", "invalid", "weather status"))
		}
		after.Weather = req.Weather
	}
	if req.Lighting != "" {
		if req.Lighting != "on" && req.Lighting != "off" {
			return before, errs.Validation("invalid_lighting", "lighting must be on or off", errs.Field("lighting", "invalid", "on or off"))
		}
		after.Lighting = req.Lighting
	}
	if req.ClosedHoles != nil {
		after.ClosedHoles = strings.ReplaceAll(*req.ClosedHoles, " ", "")
	}
	if req.Notes != nil {
		after.Notes = nullStr(*req.Notes)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.course_status (course_id, property_id, course_state, weather, lighting, closed_holes, notes, updated_at, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now(),$8) ON CONFLICT (course_id) DO UPDATE SET course_state = EXCLUDED.course_state, weather = EXCLUDED.weather,
		lighting = EXCLUDED.lighting, closed_holes = EXCLUDED.closed_holes, notes = EXCLUDED.notes, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		courseID, property, after.CourseState, after.Weather, after.Lighting, after.ClosedHoles, after.Notes, id.Ptr(actor(ctx))); err != nil {
		return before, err
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if _, err := tx.Exec(ctx, `INSERT INTO golf.course_status_log (id, property_id, course_id, before, after, actor_id) VALUES ($1,$2,$3,$4,$5,$6)`,
		id.New(), property, courseID, b, a, id.Ptr(actor(ctx))); err != nil {
		return before, err
	}
	users, err := notification.UserIDsWithPermission(ctx, tx, "golf.starter.view", &property)
	if err != nil {
		return before, err
	}
	summary := strings.ReplaceAll(after.Weather, "_", " ")
	if after.CourseState == "closed" {
		summary = "course closed"
	}
	notes := ""
	if after.Notes != nil {
		notes = *after.Notes
	}
	if err := m.Notify.Send(ctx, tx, notify.Message{Event: "golf.course_status_changed", Category: "general", UserIDs: users, PropertyID: &property,
		Channels: []string{notify.ChannelInApp}, Data: map[string]any{"course": after.CourseName, "summary": summary, "courseState": after.CourseState,
			"weather": strings.ReplaceAll(after.Weather, "_", " "), "lighting": after.Lighting, "closedHoles": after.ClosedHoles, "notes": notes}}); err != nil {
		return before, err
	}
	loc := location(ctx, tx, property)
	if err := notifyRealtime(ctx, tx, property, "golf.course_status", "updated", courseID, localDay(clock.Now(), loc), nil); err != nil {
		return before, err
	}
	out, err := loadCourseStatus(ctx, tx, courseID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionUpdate, EntityType: "golf.course_status", EntityID: courseID.String(),
		EntityLabel: after.CourseName, PropertyID: &property, Before: before, After: out})
}

// ── caddy (EP-09) ─────────────────────────────────────────────────────────

// CaddyBoardEntry is a caddy with today's availability and status.
type CaddyBoardEntry struct {
	CaddyID     uuid.UUID  `json:"caddyId"`
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	Gender      *string    `json:"gender"`
	Attendance  *string    `json:"attendance" enum:"present,absent,leave"`
	QueueNo     *float64   `json:"queueNo"`
	ArrivedAt   *time.Time `json:"arrivedAt"`
	Status      string     `json:"status" enum:"available,assigned,in_play,not_available"`
	Assignment  *uuid.UUID `json:"assignmentId"`
	FlightID    *uuid.UUID `json:"flightId"`
	TeeTime     *string    `json:"teeTime"`
	RoundsToday int        `json:"roundsToday"`
}

// CaddyBoard lists caddies for a day in queue order (Caddy Queue).
func CaddyBoard(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time, loc *time.Location) ([]CaddyBoardEntry, error) {
	rows, err := q.Query(ctx, `SELECT c.id, c.code, c.name, c.gender, a.status, a.queue_no::float8, a.arrived_at, ca.id, ca.flight_id, ca.status, lower(ca.period),
		(SELECT count(*) FROM golf.caddy_assignments x WHERE x.caddy_id = c.id AND x.play_date = $2::date AND x.status IN ('assigned','in_play','completed')),
		s.clocked_out_at IS NOT NULL
		FROM golf.caddies c
		LEFT JOIN golf.caddy_attendance a ON a.caddy_id = c.id AND a.work_date = $2::date
		LEFT JOIN golf.caddy_shifts s ON s.caddy_id = c.id AND s.work_date = $2::date
		LEFT JOIN LATERAL (SELECT id, flight_id, status, period FROM golf.caddy_assignments x WHERE x.caddy_id = c.id AND x.play_date = $2::date
			AND x.status IN ('assigned', 'in_play') ORDER BY lower(x.period) LIMIT 1) ca ON true
		WHERE c.property_id = $1 AND c.status = 'active' AND c.archived_at IS NULL
		ORDER BY (a.status = 'present') DESC NULLS LAST, a.queue_no NULLS LAST, c.code`, property, day.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CaddyBoardEntry{}
	for rows.Next() {
		var e CaddyBoardEntry
		var aStatus *string
		var start *time.Time
		var clockedOut bool
		if err := rows.Scan(&e.CaddyID, &e.Code, &e.Name, &e.Gender, &e.Attendance, &e.QueueNo, &e.ArrivedAt, &e.Assignment, &e.FlightID, &aStatus, &start, &e.RoundsToday, &clockedOut); err != nil {
			return nil, err
		}
		switch {
		case aStatus != nil && *aStatus == "in_play":
			e.Status = "in_play"
		case aStatus != nil:
			e.Status = "assigned"
		case clockedOut: // gone home (P2 clock-out, golf.caddy_shifts)
			e.Status = "not_available"
		case e.Attendance != nil && *e.Attendance == "present":
			e.Status = "available"
		default:
			e.Status = "not_available"
		}
		if start != nil {
			s := start.Add(30 * time.Minute).In(loc).Format("15:04")
			e.TeeTime = &s
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// PRD P5 FR-TRC-03: caddies without a valid mandatory certification are
	// not offered for assignment.
	var avail []uuid.UUID
	for _, e := range out {
		if e.Status == "available" {
			avail = append(avail, e.CaddyID)
		}
	}
	gaps, err := uncertifiedCaddies(ctx, q, property, avail, day)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if _, bad := gaps[out[i].CaddyID]; bad {
			out[i].Status = "not_available"
		}
	}
	return out, nil
}

// AttendanceEntry records a caddy's attendance.
type AttendanceEntry struct {
	CaddyID uuid.UUID `json:"caddyId"`
	Status  string    `json:"status" enum:"present,absent,leave"`
	Notes   string    `json:"notes,omitempty"`
}

// AttendanceRequest records attendance for a day (Caddy Attendance).
type AttendanceRequest struct {
	Date    string            `json:"date,omitempty" doc:"Default today"`
	Entries []AttendanceEntry `json:"entries"`
}

// RecordAttendance upserts attendance; present caddies join the queue in
// arrival order (Caddy Queue / Rotation, FR-CAD-02/03).
func (m *Module) RecordAttendance(ctx context.Context, tx pgx.Tx, property uuid.UUID, req AttendanceRequest) ([]CaddyBoardEntry, error) {
	loc := location(ctx, tx, property)
	day := localDay(clock.Now(), loc)
	if req.Date != "" {
		d, err := parseDate(req.Date)
		if err != nil {
			return nil, err
		}
		day = d
	}
	for i, e := range req.Entries {
		if e.Status != "present" && e.Status != "absent" && e.Status != "leave" {
			return nil, errs.Validation("invalid_attendance", "status must be present, absent or leave", errs.Field(fmt.Sprintf("entries[%d].status", i), "invalid", "present, absent or leave"))
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.caddies WHERE id = $1 AND property_id = $2)`, e.CaddyID, property).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, errs.Validation("caddy_not_found", "caddy not found", errs.Field(fmt.Sprintf("entries[%d].caddyId", i), "not_found", "caddy not found"))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_attendance (id, property_id, caddy_id, work_date, status, queue_no, arrived_at, notes, created_by, updated_by)
			VALUES ($1,$2,$3,$4::date,$5,
			  CASE WHEN $5 = 'present' THEN (SELECT coalesce(max(queue_no), 0) + 1 FROM golf.caddy_attendance WHERE property_id = $2 AND work_date = $4::date) END,
			  CASE WHEN $5 = 'present' THEN now() END, $6, $7, $7)
			ON CONFLICT (caddy_id, work_date) DO UPDATE SET status = EXCLUDED.status, notes = EXCLUDED.notes, updated_by = EXCLUDED.updated_by,
			  queue_no = CASE WHEN EXCLUDED.status = 'present' THEN coalesce(golf.caddy_attendance.queue_no, EXCLUDED.queue_no) ELSE NULL END,
			  arrived_at = CASE WHEN EXCLUDED.status = 'present' THEN coalesce(golf.caddy_attendance.arrived_at, now()) ELSE golf.caddy_attendance.arrived_at END`,
			id.New(), property, e.CaddyID, day.Format("2006-01-02"), e.Status, nullStr(e.Notes), id.Ptr(actor(ctx))); err != nil {
			return nil, err
		}
	}
	if err := realtimeBoards(ctx, tx, property, day); err != nil {
		return nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "caddy_attendance", EntityType: "golf.caddy_attendance", EntityLabel: day.Format("2006-01-02"),
		PropertyID: &property, After: req.Entries}); err != nil {
		return nil, err
	}
	return CaddyBoard(ctx, tx, property, day, loc)
}

func realtimeBoards(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) error {
	return notifyRealtime(ctx, tx, property, "golf.boards", "changed", uuid.Nil, day, nil)
}

// releaseCancelledFlights frees what is still assigned to the cancelled
// flights of a booking (cancellation, no-show, package cancellation): the
// caddies go back to the queue, the golf carts to the fleet, the flight leaves
// the starter queue and the caddy tablets stop listing it.
func releaseCancelledFlights(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID, day time.Time, reason string) error {
	caddies, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments a SET status = 'cancelled', replace_reason = coalesce(a.replace_reason, $2)
		FROM golf.flights f WHERE f.id = a.flight_id AND f.booking_id = $1 AND f.status = 'cancelled' AND a.status = 'assigned'`, bid, reason)
	if err != nil {
		return err
	}
	carts, err := tx.Exec(ctx, `UPDATE golf.golf_cart_assignments a SET status = 'cancelled', updated_at = now()
		FROM golf.flights f WHERE f.id = a.flight_id AND f.booking_id = $1 AND f.status = 'cancelled' AND a.status = 'assigned'`, bid)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.starter_queue q SET status = 'removed'
		FROM golf.flights f WHERE f.id = q.flight_id AND f.booking_id = $1 AND f.status = 'cancelled' AND q.status = 'waiting'`, bid); err != nil {
		return err
	}
	if caddies.RowsAffected()+carts.RowsAffected() == 0 {
		return nil
	}
	return realtimeBoards(ctx, tx, property, day)
}

// ReorderRequest sets the caddy rotation order manually.
type ReorderRequest struct {
	Date     string      `json:"date,omitempty"`
	CaddyIDs []uuid.UUID `json:"caddyIds" doc:"Present caddies in the new queue order"`
}

// ReorderQueue renumbers the queue (Caddy Rotation, manual order).
func (m *Module) ReorderQueue(ctx context.Context, tx pgx.Tx, property uuid.UUID, req ReorderRequest) ([]CaddyBoardEntry, error) {
	loc := location(ctx, tx, property)
	day := localDay(clock.Now(), loc)
	if req.Date != "" {
		d, err := parseDate(req.Date)
		if err != nil {
			return nil, err
		}
		day = d
	}
	for i, cid := range req.CaddyIDs {
		tag, err := tx.Exec(ctx, `UPDATE golf.caddy_attendance SET queue_no = $3 WHERE caddy_id = $1 AND work_date = $2::date AND status = 'present' AND property_id = $4`,
			cid, day.Format("2006-01-02"), i+1, property)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, errs.Validation("caddy_not_present", "only present caddies can be ordered", errs.Field(fmt.Sprintf("caddyIds[%d]", i), "invalid", "caddy not present"))
		}
	}
	if err := realtimeBoards(ctx, tx, property, day); err != nil {
		return nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "caddy_rotation", EntityType: "golf.caddy_attendance", EntityLabel: day.Format("2006-01-02"),
		PropertyID: &property, After: req.CaddyIDs}); err != nil {
		return nil, err
	}
	return CaddyBoard(ctx, tx, property, day, loc)
}

// CaddyAssignInput assigns one caddy to players.
type CaddyAssignInput struct {
	CaddyID   uuid.UUID   `json:"caddyId"`
	PlayerIDs []uuid.UUID `json:"playerIds"`
}

// CaddyAssignRequest assigns caddies to a flight (Assign Caddy).
type CaddyAssignRequest struct {
	FlightID    uuid.UUID          `json:"flightId"`
	Assignments []CaddyAssignInput `json:"assignments,omitempty"`
	Auto        bool               `json:"auto,omitempty" doc:"Take the next caddies of the queue (requests honoured first)"`
}

// CaddyAssignment is the API view.
type CaddyAssignment struct {
	ID            uuid.UUID   `json:"id"`
	CaddyID       uuid.UUID   `json:"caddyId"`
	CaddyCode     string      `json:"caddyCode"`
	CaddyName     string      `json:"caddyName"`
	FlightID      uuid.UUID   `json:"flightId"`
	PlayerIDs     []uuid.UUID `json:"playerIds"`
	PlayerNames   []string    `json:"playerNames"`
	BookingCode   *string     `json:"bookingCode"`
	PlayDate      string      `json:"playDate"`
	TeeTime       string      `json:"teeTime"`
	Status        string      `json:"status" enum:"assigned,in_play,completed,cancelled,replaced"`
	FeeAmount     string      `json:"feeAmount" doc:"Caddy fee held for the caddy (liability)"`
	Tips          string      `json:"tips"`
	Requested     bool        `json:"requested"`
	ReplaceReason *string     `json:"replaceReason"`
	AssignedAt    time.Time   `json:"assignedAt"`
	AcceptedAt    *time.Time  `json:"acceptedAt" doc:"When the caddy accepted the assignment on the tablet"`
	StartedAt     *time.Time  `json:"startedAt"`
	FinishedAt    *time.Time  `json:"finishedAt"`
	Route         *string     `json:"route"`
}

// ListCaddyAssignments lists assignments (Current Assignment / History).
func ListCaddyAssignments(ctx context.Context, q dbtx.Querier, loc *time.Location, where string, args ...any) ([]CaddyAssignment, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.caddy_id, c.code, c.name, a.flight_id, a.player_ids, b.code, a.play_date, lower(a.period), a.status, a.fee_amount::text,
		coalesce((SELECT sum(amount) FROM golf.caddy_tips t WHERE t.assignment_id = a.id), 0)::text, a.requested, a.replace_reason, a.assigned_at, a.started_at,
		a.finished_at, pr.name,
		(SELECT array_agg(bp.name ORDER BY bp.seq) FROM golf.booking_players bp WHERE bp.id = ANY(a.player_ids)),
		(SELECT max(ac.accepted_at) FROM golf.caddy_assignment_acceptances ac WHERE ac.assignment_id = a.id)
		FROM golf.caddy_assignments a JOIN golf.caddies c ON c.id = a.caddy_id JOIN golf.flights f ON f.id = a.flight_id
		LEFT JOIN golf.bookings b ON b.id = f.booking_id LEFT JOIN golf.playing_routes pr ON pr.id = b.playing_route_id
		WHERE `+where+` ORDER BY lower(a.period) DESC, a.id LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CaddyAssignment{}
	for rows.Next() {
		var a CaddyAssignment
		var day time.Time
		var start time.Time
		var names []string
		if err := rows.Scan(&a.ID, &a.CaddyID, &a.CaddyCode, &a.CaddyName, &a.FlightID, &a.PlayerIDs, &a.BookingCode, &day, &start, &a.Status, &a.FeeAmount,
			&a.Tips, &a.Requested, &a.ReplaceReason, &a.AssignedAt, &a.StartedAt, &a.FinishedAt, &a.Route, &names, &a.AcceptedAt); err != nil {
			return nil, err
		}
		a.PlayDate = day.Format("2006-01-02")
		a.TeeTime = start.Add(30 * time.Minute).In(loc).Format("15:04")
		if names == nil {
			names = []string{}
		}
		a.PlayerNames = names
		out = append(out, a)
	}
	return out, rows.Err()
}

type flightInfo struct {
	id        uuid.UUID
	bookingID *uuid.UUID
	courseID  uuid.UUID
	day       time.Time
	start     time.Time
	holes     int
	status    string
	players   []uuid.UUID
	snapshots map[uuid.UUID]*uuid.UUID
	request   *string
}

func (m *Module) loadFlightInfo(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID) (flightInfo, error) {
	fi := flightInfo{id: fid, snapshots: map[uuid.UUID]*uuid.UUID{}, holes: 18}
	var route *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT f.booking_id, f.course_id, f.play_date, t.start_at, f.status, coalesce(b.playing_route_id, t.playing_route_id), b.caddy_request
		FROM golf.flights f JOIN golf.tee_times t ON t.id = f.tee_time_id LEFT JOIN golf.bookings b ON b.id = f.booking_id
		WHERE f.id = $1 AND f.property_id = $2 FOR UPDATE OF f`, fid, property).Scan(&fi.bookingID, &fi.courseID, &fi.day, &fi.start, &fi.status, &route, &fi.request); err != nil {
		return fi, errs.NotFound("flight")
	}
	if route != nil {
		_ = tx.QueryRow(ctx, `SELECT hole_count FROM golf.playing_routes WHERE id = $1`, *route).Scan(&fi.holes)
	}
	rows, err := tx.Query(ctx, `SELECT id, pricing_snapshot_id FROM golf.booking_players WHERE flight_id = $1 AND status IN ('booked', 'checked_in') ORDER BY seq`, fid)
	if err != nil {
		return fi, err
	}
	defer rows.Close()
	for rows.Next() {
		var p uuid.UUID
		var s *uuid.UUID
		if err := rows.Scan(&p, &s); err != nil {
			return fi, err
		}
		fi.players = append(fi.players, p)
		fi.snapshots[p] = s
	}
	return fi, rows.Err()
}

func (fi flightInfo) period(pol Policies) (time.Time, time.Time) {
	mins := pol.Golf.RoundMinutes18
	if fi.holes <= 9 {
		mins = pol.Golf.RoundMinutes9
	}
	if mins <= 0 {
		mins = 300
	}
	return fi.start.Add(-30 * time.Minute), fi.start.Add(time.Duration(mins) * time.Minute)
}

// componentAmount sums one component (e.g. caddy_fee, buggy_fee) of the
// players' price snapshots.
func componentAmount(ctx context.Context, q dbtx.Querier, snaps []*uuid.UUID, code string) decimal.Decimal {
	total := decimal.Zero
	for _, s := range snaps {
		if s == nil {
			continue
		}
		snap, err := commercial.GetSnapshot(ctx, q, *s)
		if err != nil {
			continue
		}
		raw := []byte(snap.Components)
		var cs []struct {
			Code   string `json:"code"`
			Amount string `json:"amount"`
		}
		_ = json.Unmarshal(raw, &cs)
		for _, c := range cs {
			if c.Code == code {
				total = total.Add(dec(c.Amount))
			}
		}
	}
	return total
}

// AssignCaddies assigns caddies to a flight (FR-CAD-04). A caddy already
// assigned at an overlapping time is refused by the EXCLUDE constraint.
func (m *Module) AssignCaddies(ctx context.Context, tx pgx.Tx, property uuid.UUID, req CaddyAssignRequest) ([]CaddyAssignment, error) {
	pol, err := LoadPolicies(ctx, tx, property, clock.Now())
	if err != nil {
		return nil, err
	}
	fi, err := m.loadFlightInfo(ctx, tx, property, req.FlightID)
	if err != nil {
		return nil, err
	}
	if fi.status == "completed" || fi.status == "cancelled" {
		return nil, errs.Conflict("flight_closed", "the flight is "+fi.status)
	}
	loc := location(ctx, tx, property)
	assigned := map[uuid.UUID]bool{}
	rows, err := tx.Query(ctx, `SELECT unnest(player_ids) FROM golf.caddy_assignments WHERE flight_id = $1 AND status IN ('assigned', 'in_play')`, req.FlightID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p uuid.UUID
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		assigned[p] = true
	}
	rows.Close()
	inputs := req.Assignments
	if req.Auto {
		// the players' caddy preferences from the Member App: No Caddy is
		// skipped (unless a caddy is mandatory), a Preferred Caddy goes first
		pref, preferred, err := playerCaddyRequests(ctx, tx, fi.players)
		if err != nil {
			return nil, err
		}
		var open []uuid.UUID
		for _, p := range fi.players {
			if !assigned[p] && (pref[p] != "none" || pol.Caddy.Mandatory) {
				open = append(open, p)
			}
		}
		board, err := CaddyBoard(ctx, tx, property, fi.day, loc)
		if err != nil {
			return nil, err
		}
		var avail []CaddyBoardEntry
		for _, c := range board {
			if c.Status == "available" {
				avail = append(avail, c)
			}
		}
		// a requested caddy (from the booking) goes first when available
		if fi.request != nil && pol.Caddy.AllowRequest {
			sort.SliceStable(avail, func(i, j int) bool {
				ri := strings.EqualFold(avail[i].Code, *fi.request) || strings.EqualFold(avail[i].Name, *fi.request)
				rj := strings.EqualFold(avail[j].Code, *fi.request) || strings.EqualFold(avail[j].Name, *fi.request)
				return ri && !rj
			})
		}
		per := max(pol.Caddy.PlayersPerCaddy, 1)
		if pol.Caddy.AllowRequest && len(preferred) > 0 {
			var rest []uuid.UUID
			byCaddy := map[uuid.UUID][]uuid.UUID{}
			var order []uuid.UUID
			for _, p := range open {
				c, ok := preferred[p]
				if !ok {
					rest = append(rest, p)
					continue
				}
				if _, seen := byCaddy[c]; !seen {
					order = append(order, c)
				}
				byCaddy[c] = append(byCaddy[c], p)
			}
			for _, c := range order {
				at := -1
				for i := range avail {
					if avail[i].CaddyID == c {
						at = i
						break
					}
				}
				ps := byCaddy[c]
				if at < 0 {
					rest = append(rest, ps...) // not available: the queue serves them
					continue
				}
				inputs = append(inputs, CaddyAssignInput{CaddyID: c, PlayerIDs: ps[:min(per, len(ps))]})
				rest = append(rest, ps[min(per, len(ps)):]...)
				avail = append(avail[:at], avail[at+1:]...)
			}
			open = rest
		}
		for i := 0; i < len(open); i += per {
			if len(avail) == 0 {
				return nil, errs.Conflict("no_caddy_available", "no caddy is available in the queue")
			}
			end := min(i+per, len(open))
			inputs = append(inputs, CaddyAssignInput{CaddyID: avail[0].CaddyID, PlayerIDs: open[i:end]})
			avail = avail[1:]
		}
	}
	if len(inputs) == 0 && req.Auto {
		return nil, errs.Conflict("no_caddy_requested", "every player without a caddy asked for No Caddy")
	}
	if len(inputs) == 0 {
		return nil, errs.Validation("assignments_required", "choose caddies or use auto", errs.Field("assignments", "required", "one or more"))
	}
	start, end := fi.period(pol)
	var ids []uuid.UUID
	for i, in := range inputs {
		field := fmt.Sprintf("assignments[%d]", i)
		if len(in.PlayerIDs) == 0 || len(in.PlayerIDs) > max(pol.Caddy.PlayersPerCaddy, 1) {
			return nil, errs.Validation("invalid_players", fmt.Sprintf("a caddy serves 1–%d players (Caddy Policy)", max(pol.Caddy.PlayersPerCaddy, 1)),
				errs.Field(field, "invalid", "players per caddy"))
		}
		var snaps []*uuid.UUID
		for _, p := range in.PlayerIDs {
			s, ok := fi.snapshots[p]
			if !ok {
				return nil, errs.Validation("invalid_player", "player not in this flight", errs.Field(field, "invalid", "player of this flight"))
			}
			if assigned[p] {
				return nil, errs.Conflict("player_has_caddy", "a player already has a caddy; replace the assignment instead")
			}
			snaps = append(snaps, s)
		}
		var cstatus string
		var att *string
		if err := tx.QueryRow(ctx, `SELECT c.status, a.status FROM golf.caddies c LEFT JOIN golf.caddy_attendance a ON a.caddy_id = c.id AND a.work_date = $3::date
			WHERE c.id = $1 AND c.property_id = $2`, in.CaddyID, property, fi.day.Format("2006-01-02")).Scan(&cstatus, &att); err != nil {
			return nil, errs.Validation("caddy_not_found", "caddy not found", errs.Field(field, "not_found", "caddy not found"))
		}
		if cstatus != "active" || att == nil || *att != "present" {
			return nil, errs.Conflict("caddy_not_available", "the caddy is not present today (Caddy Availability)")
		}
		gaps, err := uncertifiedCaddies(ctx, tx, property, []uuid.UUID{in.CaddyID}, fi.day)
		if err != nil {
			return nil, err
		}
		if msg, bad := gaps[in.CaddyID]; bad {
			return nil, errs.Conflict("caddy_not_certified", msg)
		}
		fee := componentAmount(ctx, tx, snaps, "caddy_fee")
		aid := id.New()
		requested := fi.request != nil
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, err
		}
		_, err = sp.Exec(ctx, `INSERT INTO golf.caddy_assignments (id, property_id, caddy_id, flight_id, player_ids, play_date, period, status, fee_amount, requested, assigned_by)
			VALUES ($1,$2,$3,$4,$5,$6::date,tstzrange($7,$8,'[)'),'assigned',$9::numeric,$10,$11)`,
			aid, property, in.CaddyID, req.FlightID, in.PlayerIDs, fi.day.Format("2006-01-02"), start, end, fee.String(), requested, id.Ptr(actor(ctx)))
		if err != nil {
			_ = sp.Rollback(ctx)
			if dbtx.IsExclusionViolation(err) {
				return nil, errs.Conflict("caddy_busy", "this caddy is already assigned to a flight at an overlapping time")
			}
			return nil, err
		}
		if err := sp.Commit(ctx); err != nil {
			return nil, err
		}
		for _, p := range in.PlayerIDs {
			assigned[p] = true
		}
		ids = append(ids, aid)
	}
	if _, err := m.EvaluateReadiness(ctx, tx, property, req.FlightID); err != nil {
		return nil, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "caddy_assigned", fi.courseID, fi.day, map[string]any{"flightId": req.FlightID.String()}); err != nil {
		return nil, err
	}
	if err := realtimeBoards(ctx, tx, property, fi.day); err != nil {
		return nil, err
	}
	out, err := ListCaddyAssignments(ctx, tx, loc, "a.id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "assign_caddy", EntityType: "golf.flight", EntityID: req.FlightID.String(),
		PropertyID: &property, After: out})
}

// ReplaceRequest replaces a caddy during the day (FR-CAD-09).
type ReplaceRequest struct {
	CaddyID uuid.UUID `json:"caddyId"`
	Reason  string    `json:"reason"`
}

// ReplaceCaddy swaps the caddy of an assignment, recording the reason.
func (m *Module) ReplaceCaddy(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, req ReplaceRequest) ([]CaddyAssignment, error) {
	if strings.TrimSpace(req.Reason) == "" {
		return nil, errs.Validation("reason_required", "a reason is required", errs.Field("reason", "required", "reason"))
	}
	var flight uuid.UUID
	var players []uuid.UUID
	var status string
	if err := tx.QueryRow(ctx, `SELECT flight_id, player_ids, status FROM golf.caddy_assignments WHERE id = $1 AND property_id = $2 FOR UPDATE`, aid, property).
		Scan(&flight, &players, &status); err != nil {
		return nil, errs.NotFound("caddy assignment")
	}
	if status != "assigned" && status != "in_play" {
		return nil, errs.Conflict("assignment_closed", "only active assignments can be replaced")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET status = 'replaced', replace_reason = $2, finished_at = now(),
		period = tstzrange(lower(period), greatest(lower(period) + interval '1 minute', now()), '[)') WHERE id = $1`, aid, req.Reason); err != nil {
		return nil, err
	}
	out, err := m.AssignCaddies(ctx, tx, property, CaddyAssignRequest{FlightID: flight, Assignments: []CaddyAssignInput{{CaddyID: req.CaddyID, PlayerIDs: players}}})
	if err != nil {
		return nil, err
	}
	if status == "in_play" {
		if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET status = 'in_play', started_at = now() WHERE id = $1`, out[0].ID); err != nil {
			return nil, err
		}
		out[0].Status = "in_play"
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET replaced_by_id = $2 WHERE id = $1`, aid, out[0].ID); err != nil {
		return nil, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "replace_caddy", EntityType: "golf.caddy_assignment", EntityID: aid.String(),
		PropertyID: &property, Reason: req.Reason, After: map[string]any{"newAssignmentId": out[0].ID, "caddyId": req.CaddyID}})
}

// TipRequest adds a caddy tip to the folio (FR-CAD-08).
type TipRequest struct {
	AssignmentID uuid.UUID  `json:"assignmentId"`
	PlayerID     *uuid.UUID `json:"playerId,omitempty"`
	Amount       string     `json:"amount"`
	Method       string     `json:"method" enum:"cash,non_cash"`
}

// CaddyTip is the API view of a tip.
type CaddyTip struct {
	ID           uuid.UUID  `json:"id"`
	CaddyID      uuid.UUID  `json:"caddyId"`
	CaddyName    string     `json:"caddyName"`
	AssignmentID *uuid.UUID `json:"assignmentId"`
	Amount       string     `json:"amount"`
	Method       string     `json:"method"`
	TipDate      string     `json:"tipDate"`
	FolioLineID  *uuid.UUID `json:"folioLineId"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// AddTip posts the tip to the booking folio (liability for the caddy).
func (m *Module) AddTip(ctx context.Context, tx pgx.Tx, property uuid.UUID, req TipRequest) (CaddyTip, error) {
	amt, err := decimal.NewFromString(req.Amount)
	if err != nil || !amt.IsPositive() {
		return CaddyTip{}, errs.Validation("invalid_amount", "amount must be positive", errs.Field("amount", "invalid", "positive amount"))
	}
	if req.Method != "cash" && req.Method != "non_cash" {
		return CaddyTip{}, errs.Validation("invalid_method", "method must be cash or non_cash", errs.Field("method", "invalid", "cash or non_cash"))
	}
	var caddy, flight uuid.UUID
	var players []uuid.UUID
	var caddyName string
	var day time.Time
	if err := tx.QueryRow(ctx, `SELECT a.caddy_id, a.flight_id, a.player_ids, c.name, a.play_date FROM golf.caddy_assignments a JOIN golf.caddies c ON c.id = a.caddy_id
		WHERE a.id = $1 AND a.property_id = $2`, req.AssignmentID, property).Scan(&caddy, &flight, &players, &caddyName, &day); err != nil {
		return CaddyTip{}, errs.NotFound("caddy assignment")
	}
	var folio *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT b.folio_id FROM golf.flights f JOIN golf.bookings b ON b.id = f.booking_id WHERE f.id = $1`, flight).Scan(&folio); err != nil || folio == nil {
		return CaddyTip{}, errs.Conflict("no_folio", "the flight has no booking folio")
	}
	player := req.PlayerID
	if player == nil && len(players) > 0 {
		player = &players[0]
	}
	tid := id.New()
	lid, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: *folio, ChargeType: "caddy_tip", Description: "Caddy tip — " + caddyName + " (" + strings.ReplaceAll(req.Method, "_", "-") + ")",
		UnitPrice: amt, Net: amt, Total: amt, ReferenceType: "golf_caddy", ReferenceID: &caddy, Liability: true})
	if err != nil {
		return CaddyTip{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_tips (id, property_id, caddy_id, assignment_id, booking_player_id, amount, currency, method, folio_line_id, tip_date, created_by)
		VALUES ($1,$2,$3,$4,$5,$6::numeric,'IDR',$7,$8,$9::date,$10)`, tid, property, caddy, req.AssignmentID, player, amt.String(), req.Method, lid,
		day.Format("2006-01-02"), id.Ptr(actor(ctx))); err != nil {
		return CaddyTip{}, err
	}
	out := CaddyTip{ID: tid, CaddyID: caddy, CaddyName: caddyName, AssignmentID: &req.AssignmentID, Amount: amt.StringFixed(0), Method: req.Method,
		TipDate: day.Format("2006-01-02"), FolioLineID: &lid, CreatedAt: clock.Now()}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "caddy_tip", EntityType: "golf.caddy_tip", EntityID: tid.String(), EntityLabel: caddyName,
		PropertyID: &property, After: out})
}

// ── golf carts (EP-10) ────────────────────────────────────────────────────

// CartBoardEntry is a golf cart with readiness and current assignment.
type CartBoardEntry struct {
	ID          uuid.UUID  `json:"id"`
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	CartType    string     `json:"cartType"`
	Capacity    int        `json:"capacity"`
	Readiness   string     `json:"readiness" enum:"ready,not_ready,in_use,charging,maintenance,out_of_service"`
	Reason      *string    `json:"readinessReason"`
	ChangedAt   time.Time  `json:"readinessChangedAt"`
	Assignment  *uuid.UUID `json:"assignmentId"`
	FlightID    *uuid.UUID `json:"flightId"`
	RoundsToday int        `json:"roundsToday"`
}

// CartBoard lists active golf carts.
func CartBoard(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) ([]CartBoardEntry, error) {
	rows, err := q.Query(ctx, `SELECT c.id, c.code, c.name, c.cart_type, c.capacity, c.readiness, c.readiness_reason, c.readiness_changed_at, a.id, a.flight_id,
		(SELECT count(*) FROM golf.golf_cart_assignments x WHERE x.golf_cart_id = c.id AND x.play_date = $2::date AND x.status <> 'cancelled')
		FROM golf.golf_carts c LEFT JOIN LATERAL (SELECT id, flight_id FROM golf.golf_cart_assignments x WHERE x.golf_cart_id = c.id
			AND x.status IN ('assigned', 'in_use') ORDER BY x.assigned_at DESC LIMIT 1) a ON true
		WHERE c.property_id = $1 AND c.status = 'active' AND c.archived_at IS NULL ORDER BY c.code`, property, day.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CartBoardEntry{}
	for rows.Next() {
		var e CartBoardEntry
		if err := rows.Scan(&e.ID, &e.Code, &e.Name, &e.CartType, &e.Capacity, &e.Readiness, &e.Reason, &e.ChangedAt, &e.Assignment, &e.FlightID, &e.RoundsToday); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReadinessRequest changes a golf cart's readiness (FR-CRT-02/08).
type ReadinessRequest struct {
	Readiness string `json:"readiness" enum:"ready,not_ready,charging,maintenance,out_of_service"`
	Reason    string `json:"reason,omitempty" doc:"Required for Maintenance and Out of Service"`
}

// SetReadiness updates a golf cart's readiness state.
func (m *Module) SetReadiness(ctx context.Context, tx pgx.Tx, property, cartID uuid.UUID, req ReadinessRequest) (CartBoardEntry, error) {
	switch req.Readiness {
	case "ready", "not_ready", "charging", "maintenance", "out_of_service":
	default:
		return CartBoardEntry{}, errs.Validation("invalid_readiness", "invalid readiness", errs.Field("readiness", "invalid", "ready, not_ready, charging, maintenance or out_of_service"))
	}
	if (req.Readiness == "maintenance" || req.Readiness == "out_of_service") && strings.TrimSpace(req.Reason) == "" {
		return CartBoardEntry{}, errs.Validation("reason_required", "a reason is required for Maintenance and Out of Service", errs.Field("reason", "required", "reason"))
	}
	var before string
	if err := tx.QueryRow(ctx, `SELECT readiness FROM golf.golf_carts WHERE id = $1 AND property_id = $2 FOR UPDATE`, cartID, property).Scan(&before); err != nil {
		return CartBoardEntry{}, errs.NotFound("golf cart")
	}
	if before == "in_use" {
		return CartBoardEntry{}, errs.Conflict("cart_in_use", "the golf cart is in use; return it first")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.golf_carts SET readiness = $2, readiness_reason = $3, readiness_changed_at = now(), updated_by = $4 WHERE id = $1`,
		cartID, req.Readiness, nullStr(req.Reason), id.Ptr(actor(ctx))); err != nil {
		return CartBoardEntry{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.golf_cart_events (id, property_id, golf_cart_id, from_state, to_state, reason, actor_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id.New(), property, cartID, before, req.Readiness, nullStr(req.Reason), id.Ptr(actor(ctx))); err != nil {
		return CartBoardEntry{}, err
	}
	day := localDay(clock.Now(), location(ctx, tx, property))
	if err := realtimeBoards(ctx, tx, property, day); err != nil {
		return CartBoardEntry{}, err
	}
	board, err := CartBoard(ctx, tx, property, day)
	if err != nil {
		return CartBoardEntry{}, err
	}
	var out CartBoardEntry
	for _, e := range board {
		if e.ID == cartID {
			out = e
		}
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionStatusChange, EntityType: "golf.golf_cart", EntityID: cartID.String(),
		EntityLabel: out.Code, PropertyID: &property, Reason: req.Reason, Before: map[string]any{"readiness": before}, After: map[string]any{"readiness": req.Readiness}})
}

// CartAssignRequest assigns golf carts to a flight.
type CartAssignRequest struct {
	FlightID uuid.UUID   `json:"flightId"`
	CartIDs  []uuid.UUID `json:"golfCartIds,omitempty"`
	Auto     bool        `json:"auto,omitempty" doc:"Take ready golf carts for the buggy sharing rule"`
}

// CartAssignment is the API view.
type CartAssignment struct {
	ID          uuid.UUID  `json:"id"`
	GolfCartID  uuid.UUID  `json:"golfCartId"`
	CartCode    string     `json:"golfCartCode"`
	FlightID    uuid.UUID  `json:"flightId"`
	BookingCode *string    `json:"bookingCode"`
	PlayDate    string     `json:"playDate"`
	Status      string     `json:"status" enum:"assigned,in_use,returned,cancelled"`
	Extra       bool       `json:"extra" doc:"Beyond the buggy sharing rule (surcharge)"`
	FeeAmount   string     `json:"feeAmount"`
	AssignedAt  time.Time  `json:"assignedAt"`
	OutAt       *time.Time `json:"outAt"`
	ReturnedAt  *time.Time `json:"returnedAt"`
}

// ListCartAssignments lists assignments (Basic Usage History).
func ListCartAssignments(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]CartAssignment, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.golf_cart_id, c.code, a.flight_id, b.code, a.play_date, a.status, a.extra, a.fee_amount::text, a.assigned_at, a.out_at, a.returned_at
		FROM golf.golf_cart_assignments a JOIN golf.golf_carts c ON c.id = a.golf_cart_id JOIN golf.flights f ON f.id = a.flight_id
		LEFT JOIN golf.bookings b ON b.id = f.booking_id WHERE `+where+` ORDER BY a.assigned_at DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CartAssignment{}
	for rows.Next() {
		var a CartAssignment
		var d time.Time
		if err := rows.Scan(&a.ID, &a.GolfCartID, &a.CartCode, &a.FlightID, &a.BookingCode, &d, &a.Status, &a.Extra, &a.FeeAmount, &a.AssignedAt, &a.OutAt, &a.ReturnedAt); err != nil {
			return nil, err
		}
		a.PlayDate = d.Format("2006-01-02")
		out = append(out, a)
	}
	return out, rows.Err()
}

// AssignCarts assigns golf carts; carts beyond the sharing rule add a
// surcharge to the folio (FR-CRT-03..06). Only Ready carts can be assigned.
func (m *Module) AssignCarts(ctx context.Context, tx pgx.Tx, property uuid.UUID, req CartAssignRequest) ([]CartAssignment, error) {
	pol, err := LoadPolicies(ctx, tx, property, clock.Now())
	if err != nil {
		return nil, err
	}
	fi, err := m.loadFlightInfo(ctx, tx, property, req.FlightID)
	if err != nil {
		return nil, err
	}
	if fi.status == "completed" || fi.status == "cancelled" {
		return nil, errs.Conflict("flight_closed", "the flight is "+fi.status)
	}
	var existing int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM golf.golf_cart_assignments WHERE flight_id = $1 AND status IN ('assigned', 'in_use')`, req.FlightID).Scan(&existing); err != nil {
		return nil, err
	}
	needed := ceilDiv(len(fi.players), pol.Cart.PlayersPerCart)
	carts := req.CartIDs
	start, end := fi.period(pol)
	if req.Auto {
		var want int
		var cartReq *int
		if fi.bookingID != nil {
			_ = tx.QueryRow(ctx, `SELECT cart_request FROM golf.bookings WHERE id = $1`, *fi.bookingID).Scan(&cartReq)
		}
		want = needed
		if cartReq != nil && *cartReq > want {
			want = *cartReq
		}
		want -= existing
		// Ready carts not already committed to an overlapping round
		rows, err := tx.Query(ctx, `SELECT c.id FROM golf.golf_carts c WHERE c.property_id = $1 AND c.status = 'active' AND c.readiness = 'ready'
			AND c.archived_at IS NULL AND NOT EXISTS (SELECT 1 FROM golf.golf_cart_assignments a WHERE a.golf_cart_id = c.id
			AND a.status IN ('assigned', 'in_use') AND a.period && tstzrange($3, $4, '[)'))
			ORDER BY c.readiness_changed_at, c.code LIMIT $2`, property, max(want, 0), start, end)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c uuid.UUID
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return nil, err
			}
			carts = append(carts, c)
		}
		rows.Close()
		if len(carts) < want {
			return nil, errs.Conflict("no_cart_ready", "not enough golf carts are Ready")
		}
	}
	if len(carts) == 0 {
		return nil, errs.Validation("carts_required", "choose golf carts or use auto", errs.Field("golfCartIds", "required", "one or more"))
	}
	var snaps []*uuid.UUID
	for _, p := range fi.players {
		snaps = append(snaps, fi.snapshots[p])
	}
	buggy := componentAmount(ctx, tx, snaps, "buggy_fee")
	perCart := decimal.Zero
	if needed > 0 {
		perCart = buggy.Div(decimal.NewFromInt(int64(needed))).Round(0)
	}
	var ids []uuid.UUID
	extraCount := 0
	for i, cid := range carts {
		var readiness, status string
		if err := tx.QueryRow(ctx, `SELECT readiness, status FROM golf.golf_carts WHERE id = $1 AND property_id = $2 FOR UPDATE`, cid, property).Scan(&readiness, &status); err != nil {
			return nil, errs.Validation("cart_not_found", "golf cart not found", errs.Field(fmt.Sprintf("golfCartIds[%d]", i), "not_found", "golf cart not found"))
		}
		if status != "active" || readiness != "ready" {
			return nil, errs.Conflict("cart_not_ready", "golf cart is "+strings.ReplaceAll(readiness, "_", " ")+"; only Ready carts can be assigned")
		}
		extra := existing+i+1 > needed
		fee := perCart
		if extra {
			fee = decimal.Zero
			extraCount++
		}
		aid := id.New()
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, err
		}
		_, err = sp.Exec(ctx, `INSERT INTO golf.golf_cart_assignments (id, property_id, golf_cart_id, flight_id, player_ids, play_date, period, status, extra, fee_amount, assigned_by)
			VALUES ($1,$2,$3,$4,$5,$6::date,tstzrange($7,$8,'[)'),'assigned',$9,$10::numeric,$11)`,
			aid, property, cid, req.FlightID, fi.players, fi.day.Format("2006-01-02"), start, end, extra, fee.String(), id.Ptr(actor(ctx)))
		if err != nil {
			_ = sp.Rollback(ctx)
			if dbtx.IsExclusionViolation(err) {
				return nil, errs.Conflict("cart_busy", "this golf cart is already assigned at an overlapping time")
			}
			return nil, err
		}
		if err := sp.Commit(ctx); err != nil {
			return nil, err
		}
		ids = append(ids, aid)
	}
	// surcharge for carts beyond the sharing rule when the booking did not
	// already pay for them
	if extraCount > 0 && fi.bookingID != nil {
		var cartReq *int
		var folio *uuid.UUID
		var channel string
		_ = tx.QueryRow(ctx, `SELECT cart_request, folio_id, channel FROM golf.bookings WHERE id = $1`, *fi.bookingID).Scan(&cartReq, &folio, &channel)
		prepaidExtra := 0
		if cartReq != nil && *cartReq > needed {
			prepaidExtra = *cartReq - needed
		}
		charge := extraCount - max(prepaidExtra-max(existing-needed, 0), 0)
		if charge > 0 && folio != nil {
			var tt uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT tee_time_id FROM golf.flights WHERE id = $1`, req.FlightID).Scan(&tt); err != nil {
				return nil, err
			}
			s, err := lockSlot(ctx, tx, property, tt)
			if err != nil {
				return nil, err
			}
			var seg string
			_ = tx.QueryRow(ctx, `SELECT segment FROM golf.booking_players WHERE flight_id = $1 AND status IN ('booked','checked_in') ORDER BY seq LIMIT 1`, req.FlightID).Scan(&seg)
			segs := []string{seg}
			if seg != "member" {
				segs = []string{seg, "non_member", "guest"}
			}
			if _, err := m.optionalCharge(ctx, tx, property, s, location(ctx, tx, property), "extra_cart", segs, charge, *folio, req.FlightID, "golf_flight",
				fmt.Sprintf("Golf cart surcharge × %d", charge), channel); err != nil {
				return nil, err
			}
		}
	}
	if _, err := m.EvaluateReadiness(ctx, tx, property, req.FlightID); err != nil {
		return nil, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "cart_assigned", fi.courseID, fi.day, map[string]any{"flightId": req.FlightID.String()}); err != nil {
		return nil, err
	}
	if err := realtimeBoards(ctx, tx, property, fi.day); err != nil {
		return nil, err
	}
	out, err := ListCartAssignments(ctx, tx, "a.id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "assign_golf_cart", EntityType: "golf.flight", EntityID: req.FlightID.String(),
		PropertyID: &property, After: out})
}

// ReturnCart records a golf cart coming back (Not Ready / Charging until
// marked Ready).
func (m *Module) ReturnCart(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID) (CartAssignment, error) {
	var cart uuid.UUID
	var status string
	var day time.Time
	if err := tx.QueryRow(ctx, `SELECT golf_cart_id, status, play_date FROM golf.golf_cart_assignments WHERE id = $1 AND property_id = $2 FOR UPDATE`, aid, property).
		Scan(&cart, &status, &day); err != nil {
		return CartAssignment{}, errs.NotFound("golf cart assignment")
	}
	if status != "assigned" && status != "in_use" {
		return CartAssignment{}, errs.Conflict("assignment_closed", "the golf cart is already returned")
	}
	pol, err := LoadPolicies(ctx, tx, property, clock.Now())
	if err != nil {
		return CartAssignment{}, err
	}
	after := "not_ready"
	if pol.Cart.AfterReturn == "charging" {
		after = "charging"
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.golf_cart_assignments SET status = 'returned', returned_at = now(),
		period = tstzrange(lower(period), greatest(lower(period) + interval '1 minute', now()), '[)') WHERE id = $1`, aid); err != nil {
		return CartAssignment{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.golf_carts SET readiness = CASE WHEN cart_type = 'electric' THEN $2 ELSE 'not_ready' END, readiness_changed_at = now() WHERE id = $1`,
		cart, after); err != nil {
		return CartAssignment{}, err
	}
	if err := realtimeBoards(ctx, tx, property, day); err != nil {
		return CartAssignment{}, err
	}
	list, err := ListCartAssignments(ctx, tx, "a.id = $1", aid)
	if err != nil || len(list) == 0 {
		return CartAssignment{}, err
	}
	return list[0], audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "return_golf_cart", EntityType: "golf.golf_cart_assignment", EntityID: aid.String(),
		PropertyID: &property, After: list[0]})
}

// ── lockers, bag drop, bag storage (FR-CHK-03..05) ────────────────────────

// LockerAssignRequest assigns a locker.
type LockerAssignRequest struct {
	LockerID        uuid.UUID  `json:"lockerId"`
	BookingPlayerID *uuid.UUID `json:"bookingPlayerId,omitempty"`
	CustomerID      *uuid.UUID `json:"customerId,omitempty"`
	HolderName      string     `json:"holderName,omitempty"`
	AssignmentType  string     `json:"assignmentType" enum:"daily,periodic"`
	EndsOn          string     `json:"endsOn,omitempty" doc:"Periodic rental end date"`
	Fee             string     `json:"fee,omitempty" doc:"Periodic rental fee posted to a folio"`
}

// LockerAssignment is the API view.
type LockerAssignment struct {
	ID              uuid.UUID  `json:"id"`
	LockerID        uuid.UUID  `json:"lockerId"`
	LockerCode      string     `json:"lockerCode"`
	Area            string     `json:"area"`
	HolderName      string     `json:"holderName"`
	CustomerID      *uuid.UUID `json:"customerId"`
	BookingPlayerID *uuid.UUID `json:"bookingPlayerId"`
	AssignmentType  string     `json:"assignmentType"`
	StartsAt        time.Time  `json:"startsAt"`
	EndsAt          *time.Time `json:"endsAt"`
	Status          string     `json:"status" enum:"active,released"`
	ReleasedAt      *time.Time `json:"releasedAt"`
}

func listLockerAssignments(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]LockerAssignment, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.locker_id, l.code, l.area, a.holder_name, a.customer_id, a.booking_player_id, a.assignment_type, lower(a.period),
		CASE WHEN upper_inf(a.period) THEN NULL ELSE upper(a.period) END, a.status, a.released_at
		FROM golf.locker_assignments a JOIN golf.lockers l ON l.id = a.locker_id WHERE `+where+` ORDER BY lower(a.period) DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LockerAssignment{}
	for rows.Next() {
		var a LockerAssignment
		if err := rows.Scan(&a.ID, &a.LockerID, &a.LockerCode, &a.Area, &a.HolderName, &a.CustomerID, &a.BookingPlayerID, &a.AssignmentType, &a.StartsAt, &a.EndsAt,
			&a.Status, &a.ReleasedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AssignLocker assigns a locker for the day or a period.
func (m *Module) AssignLocker(ctx context.Context, tx pgx.Tx, property uuid.UUID, req LockerAssignRequest) (LockerAssignment, error) {
	if req.AssignmentType != "daily" && req.AssignmentType != "periodic" {
		return LockerAssignment{}, errs.Validation("invalid_type", "assignment type must be daily or periodic", errs.Field("assignmentType", "invalid", "daily or periodic"))
	}
	var lstatus, status, code string
	if err := tx.QueryRow(ctx, `SELECT locker_status, status, code FROM golf.lockers WHERE id = $1 AND property_id = $2 FOR UPDATE`, req.LockerID, property).
		Scan(&lstatus, &status, &code); err != nil {
		return LockerAssignment{}, errs.Validation("locker_not_found", "locker not found", errs.Field("lockerId", "not_found", "locker not found"))
	}
	if status != "active" || lstatus != "available" {
		return LockerAssignment{}, errs.Conflict("locker_not_available", "locker "+code+" is "+lstatus)
	}
	holder := strings.TrimSpace(req.HolderName)
	cust := req.CustomerID
	if req.BookingPlayerID != nil {
		var name string
		var c *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT name, customer_id FROM golf.booking_players WHERE id = $1 AND property_id = $2`, *req.BookingPlayerID, property).Scan(&name, &c); err != nil {
			return LockerAssignment{}, errs.Validation("player_not_found", "player not found", errs.Field("bookingPlayerId", "not_found", "player not found"))
		}
		if holder == "" {
			holder = name
		}
		if cust == nil {
			cust = c
		}
	}
	if holder == "" && cust != nil {
		n, _, _, _ := crm.Contact(ctx, tx, cust, nil)
		holder = n
	}
	if holder == "" {
		return LockerAssignment{}, errs.Validation("holder_required", "holder name is required", errs.Field("holderName", "required", "name"))
	}
	loc := location(ctx, tx, property)
	now := clock.Now()
	var end *time.Time
	if req.AssignmentType == "daily" {
		e := localDay(now, loc).AddDate(0, 0, 1)
		ee := time.Date(e.Year(), e.Month(), e.Day(), 0, 0, 0, 0, loc)
		end = &ee
	} else if req.EndsOn != "" {
		d, err := parseDate(req.EndsOn)
		if err != nil {
			return LockerAssignment{}, err
		}
		ee := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
		end = &ee
	}
	aid := id.New()
	sp, err := tx.Begin(ctx)
	if err != nil {
		return LockerAssignment{}, err
	}
	_, err = sp.Exec(ctx, `INSERT INTO golf.locker_assignments (id, property_id, locker_id, customer_id, booking_player_id, holder_name, assignment_type, period, status, fee_amount, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,tstzrange($8,$9,'[)'),'active',$10::numeric,$11)`, aid, property, req.LockerID, cust, req.BookingPlayerID, holder, req.AssignmentType,
		now, end, nullDec(req.Fee), id.Ptr(actor(ctx)))
	if err != nil {
		_ = sp.Rollback(ctx)
		if dbtx.IsExclusionViolation(err) {
			return LockerAssignment{}, errs.Conflict("locker_busy", "locker "+code+" is already assigned")
		}
		return LockerAssignment{}, err
	}
	if err := sp.Commit(ctx); err != nil {
		return LockerAssignment{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.lockers SET locker_status = 'occupied' WHERE id = $1`, req.LockerID); err != nil {
		return LockerAssignment{}, err
	}
	if fee := dec(req.Fee); fee.IsPositive() {
		// a player's locker goes on the booking folio (one bill at check-out)
		var folioID *uuid.UUID
		if req.BookingPlayerID != nil {
			_ = tx.QueryRow(ctx, `SELECT b.folio_id FROM golf.booking_players bp JOIN golf.bookings b ON b.id = bp.booking_id JOIN billing.folios f ON f.id = b.folio_id
				WHERE bp.id = $1 AND f.status = 'open' AND b.checked_out_at IS NULL`, *req.BookingPlayerID).Scan(&folioID)
		}
		if folioID == nil {
			folio, err := m.Billing.OpenFolio(ctx, tx, billing.FolioInput{Property: property, CustomerID: cust, HolderName: holder, SourceType: "other", SourceRef: "Locker " + code})
			if err != nil {
				return LockerAssignment{}, err
			}
			folioID = &folio.ID
		}
		lid, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: *folioID, ChargeType: "locker", Description: "Locker rental " + code, UnitPrice: fee, Net: fee, Total: fee,
			ReferenceType: "golf_locker", ReferenceID: &aid})
		if err != nil {
			return LockerAssignment{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.locker_assignments SET folio_line_id = $2 WHERE id = $1`, aid, lid); err != nil {
			return LockerAssignment{}, err
		}
	}
	if err := realtimeBoards(ctx, tx, property, localDay(now, loc)); err != nil {
		return LockerAssignment{}, err
	}
	list, err := listLockerAssignments(ctx, tx, "a.id = $1", aid)
	if err != nil || len(list) == 0 {
		return LockerAssignment{}, err
	}
	return list[0], audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "assign_locker", EntityType: "golf.locker_assignment", EntityID: aid.String(),
		EntityLabel: code + " · " + holder, PropertyID: &property, After: list[0]})
}

func nullDec(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if _, err := decimal.NewFromString(s); err != nil {
		return nil
	}
	return &s
}

// ReleaseLocker frees a locker.
func (m *Module) ReleaseLocker(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID) (LockerAssignment, error) {
	var locker uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE golf.locker_assignments SET status = 'released', released_at = now(),
		period = tstzrange(lower(period), greatest(lower(period) + interval '1 minute', now()), '[)')
		WHERE id = $1 AND property_id = $2 AND status = 'active' RETURNING locker_id`, aid, property).Scan(&locker)
	if dbtx.IsNoRows(err) {
		return LockerAssignment{}, errs.NotFound("active locker assignment")
	}
	if err != nil {
		return LockerAssignment{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.lockers SET locker_status = 'available' WHERE id = $1 AND locker_status = 'occupied'`, locker); err != nil {
		return LockerAssignment{}, err
	}
	if err := realtimeBoards(ctx, tx, property, localDay(clock.Now(), location(ctx, tx, property))); err != nil {
		return LockerAssignment{}, err
	}
	list, err := listLockerAssignments(ctx, tx, "a.id = $1", aid)
	if err != nil || len(list) == 0 {
		return LockerAssignment{}, err
	}
	return list[0], audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "release_locker", EntityType: "golf.locker_assignment", EntityID: aid.String(),
		PropertyID: &property, After: list[0]})
}

// BagDropRequest records a dropped bag.
type BagDropRequest struct {
	BookingPlayerID uuid.UUID `json:"bookingPlayerId"`
	TagNumber       string    `json:"tagNumber"`
	BagCount        int       `json:"bagCount,omitempty"`
	Notes           string    `json:"notes,omitempty"`
}

// BagDrop is the API view.
type BagDrop struct {
	ID              uuid.UUID  `json:"id"`
	BookingPlayerID uuid.UUID  `json:"bookingPlayerId"`
	PlayerName      string     `json:"playerName"`
	FlightID        uuid.UUID  `json:"flightId"`
	BookingCode     string     `json:"bookingCode"`
	TagNumber       string     `json:"tagNumber"`
	BagCount        int        `json:"bagCount"`
	Status          string     `json:"status" enum:"dropped,loaded,collected"`
	DroppedAt       time.Time  `json:"droppedAt"`
	CollectedAt     *time.Time `json:"collectedAt"`
	Notes           *string    `json:"notes"`
}

func listBagDrops(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]BagDrop, error) {
	rows, err := q.Query(ctx, `SELECT d.id, d.booking_player_id, bp.name, d.flight_id, b.code, d.tag_number, d.bag_count, d.status, d.dropped_at, d.collected_at, d.notes
		FROM golf.bag_drops d JOIN golf.booking_players bp ON bp.id = d.booking_player_id JOIN golf.bookings b ON b.id = bp.booking_id
		WHERE `+where+` ORDER BY d.dropped_at DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BagDrop{}
	for rows.Next() {
		var d BagDrop
		if err := rows.Scan(&d.ID, &d.BookingPlayerID, &d.PlayerName, &d.FlightID, &d.BookingCode, &d.TagNumber, &d.BagCount, &d.Status, &d.DroppedAt, &d.CollectedAt, &d.Notes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DropBag records a bag tag for a player (Bag Drop).
func (m *Module) DropBag(ctx context.Context, tx pgx.Tx, property uuid.UUID, req BagDropRequest) (BagDrop, error) {
	if strings.TrimSpace(req.TagNumber) == "" {
		return BagDrop{}, errs.Validation("tag_required", "bag tag number is required", errs.Field("tagNumber", "required", "tag number"))
	}
	if req.BagCount <= 0 {
		req.BagCount = 1
	}
	var flight uuid.UUID
	var day time.Time
	if err := tx.QueryRow(ctx, `SELECT bp.flight_id, f.play_date FROM golf.booking_players bp JOIN golf.flights f ON f.id = bp.flight_id
		WHERE bp.id = $1 AND bp.property_id = $2`, req.BookingPlayerID, property).Scan(&flight, &day); err != nil {
		return BagDrop{}, errs.Validation("player_not_found", "player not found", errs.Field("bookingPlayerId", "not_found", "player not found"))
	}
	did := id.New()
	_, err := tx.Exec(ctx, `INSERT INTO golf.bag_drops (id, property_id, booking_player_id, flight_id, play_date, tag_number, bag_count, status, notes, created_by)
		VALUES ($1,$2,$3,$4,$5::date,$6,$7,'dropped',$8,$9)`, did, property, req.BookingPlayerID, flight, day.Format("2006-01-02"), strings.ToUpper(strings.TrimSpace(req.TagNumber)),
		req.BagCount, nullStr(req.Notes), id.Ptr(actor(ctx)))
	if ok, _ := dbtx.IsUniqueViolation(err); ok {
		return BagDrop{}, errs.Validation("tag_taken", "this bag tag is already used today", errs.Field("tagNumber", "taken", "tag already used"))
	}
	if err != nil {
		return BagDrop{}, err
	}
	var course uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT course_id FROM golf.flights WHERE id = $1`, flight).Scan(&course)
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "bag_drop", course, day, map[string]any{"flightId": flight.String()}); err != nil {
		return BagDrop{}, err
	}
	list, err := listBagDrops(ctx, tx, "d.id = $1", did)
	if err != nil || len(list) == 0 {
		return BagDrop{}, err
	}
	return list[0], audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "bag_drop", EntityType: "golf.bag_drop", EntityID: did.String(), EntityLabel: req.TagNumber,
		PropertyID: &property, After: list[0]})
}

// CollectBag closes a bag drop.
func (m *Module) CollectBag(ctx context.Context, tx pgx.Tx, property, did uuid.UUID) (BagDrop, error) {
	tag, err := tx.Exec(ctx, `UPDATE golf.bag_drops SET status = 'collected', collected_at = now() WHERE id = $1 AND property_id = $2 AND status <> 'collected'`, did, property)
	if err != nil {
		return BagDrop{}, err
	}
	if tag.RowsAffected() == 0 {
		return BagDrop{}, errs.NotFound("bag drop")
	}
	list, err := listBagDrops(ctx, tx, "d.id = $1", did)
	if err != nil || len(list) == 0 {
		return BagDrop{}, err
	}
	return list[0], audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "bag_collect", EntityType: "golf.bag_drop", EntityID: did.String(), PropertyID: &property})
}

// BagStorageRequest stores a bag long-term.
type BagStorageRequest struct {
	CustomerID uuid.UUID `json:"customerId"`
	RackNumber string    `json:"rackNumber"`
	StartsOn   string    `json:"startsOn,omitempty"`
	EndsOn     string    `json:"endsOn,omitempty"`
	Fee        string    `json:"fee,omitempty"`
	Notes      string    `json:"notes,omitempty"`
}

// BagStorage is the API view.
type BagStorage struct {
	ID         uuid.UUID  `json:"id"`
	CustomerID uuid.UUID  `json:"customerId"`
	HolderName string     `json:"holderName"`
	RackNumber string     `json:"rackNumber"`
	StartsOn   string     `json:"startsOn"`
	EndsOn     *string    `json:"endsOn"`
	FeeAmount  *string    `json:"feeAmount"`
	FolioID    *uuid.UUID `json:"folioId"`
	Status     string     `json:"status" enum:"active,ended"`
	Notes      *string    `json:"notes"`
}

func listBagStorage(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]BagStorage, error) {
	rows, err := q.Query(ctx, `SELECT id, customer_id, rack_number, starts_on, ends_on, fee_amount::text, folio_id, status, notes FROM golf.bag_storage
		WHERE `+where+` ORDER BY rack_number LIMIT 1000`, args...)
	if err != nil {
		return nil, err
	}
	out := []BagStorage{}
	var cust []uuid.UUID
	for rows.Next() {
		var b BagStorage
		var s time.Time
		var e *time.Time
		if err := rows.Scan(&b.ID, &b.CustomerID, &b.RackNumber, &s, &e, &b.FeeAmount, &b.FolioID, &b.Status, &b.Notes); err != nil {
			rows.Close()
			return nil, err
		}
		b.StartsOn = s.Format("2006-01-02")
		if e != nil {
			v := e.Format("2006-01-02")
			b.EndsOn = &v
		}
		out = append(out, b)
		cust = append(cust, b.CustomerID)
	}
	rows.Close()
	names, err := crm.Names(ctx, q, cust)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].HolderName = names[out[i].CustomerID]
	}
	return out, nil
}

// StoreBag starts a bag storage (with an optional fee folio).
func (m *Module) StoreBag(ctx context.Context, tx pgx.Tx, property uuid.UUID, req BagStorageRequest) (BagStorage, error) {
	if strings.TrimSpace(req.RackNumber) == "" {
		return BagStorage{}, errs.Validation("rack_required", "rack number is required", errs.Field("rackNumber", "required", "rack number"))
	}
	ok, err := crm.ExistsInProperty(ctx, tx, property, req.CustomerID)
	if err != nil {
		return BagStorage{}, err
	}
	if !ok {
		return BagStorage{}, errs.Validation("customer_not_found", "customer not found", errs.Field("customerId", "not_found", "customer not found"))
	}
	start := localDay(clock.Now(), location(ctx, tx, property))
	if req.StartsOn != "" {
		if start, err = parseDate(req.StartsOn); err != nil {
			return BagStorage{}, err
		}
	}
	var ends *string
	if req.EndsOn != "" {
		if _, err := parseDate(req.EndsOn); err != nil {
			return BagStorage{}, err
		}
		ends = &req.EndsOn
	}
	sid := id.New()
	_, err = tx.Exec(ctx, `INSERT INTO golf.bag_storage (id, property_id, customer_id, rack_number, starts_on, ends_on, fee_amount, status, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5::date,$6::date,$7::numeric,'active',$8,$9,$9)`, sid, property, req.CustomerID, strings.ToUpper(strings.TrimSpace(req.RackNumber)),
		start.Format("2006-01-02"), ends, nullDec(req.Fee), nullStr(req.Notes), id.Ptr(actor(ctx)))
	if ok, _ := dbtx.IsUniqueViolation(err); ok {
		return BagStorage{}, errs.Validation("rack_taken", "this rack is already in use", errs.Field("rackNumber", "taken", "rack in use"))
	}
	if err != nil {
		return BagStorage{}, err
	}
	if fee := dec(req.Fee); fee.IsPositive() {
		name, _, _, _ := crm.Contact(ctx, tx, &req.CustomerID, nil)
		folio, err := m.Billing.OpenFolio(ctx, tx, billing.FolioInput{Property: property, CustomerID: &req.CustomerID, HolderName: name, SourceType: "bag_storage",
			SourceID: &sid, SourceRef: "Rack " + req.RackNumber})
		if err != nil {
			return BagStorage{}, err
		}
		if _, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: folio.ID, ChargeType: "bag_storage", Description: "Bag storage rack " + req.RackNumber,
			UnitPrice: fee, Net: fee, Total: fee, ReferenceType: "golf_bag_storage", ReferenceID: &sid}); err != nil {
			return BagStorage{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.bag_storage SET folio_id = $2 WHERE id = $1`, sid, folio.ID); err != nil {
			return BagStorage{}, err
		}
	}
	list, err := listBagStorage(ctx, tx, "id = $1", sid)
	if err != nil || len(list) == 0 {
		return BagStorage{}, err
	}
	return list[0], audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionCreate, EntityType: "golf.bag_storage", EntityID: sid.String(),
		EntityLabel: "Rack " + req.RackNumber, PropertyID: &property, After: list[0]})
}

// EndBagStorage ends a storage.
func (m *Module) EndBagStorage(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (BagStorage, error) {
	tag, err := tx.Exec(ctx, `UPDATE golf.bag_storage SET status = 'ended', ends_on = coalesce(ends_on, billing.local_date($2)), updated_by = $3 WHERE id = $1 AND property_id = $2 AND status = 'active'`,
		sid, property, id.Ptr(actor(ctx)))
	if err != nil {
		return BagStorage{}, err
	}
	if tag.RowsAffected() == 0 {
		return BagStorage{}, errs.NotFound("active bag storage")
	}
	list, err := listBagStorage(ctx, tx, "id = $1", sid)
	if err != nil || len(list) == 0 {
		return BagStorage{}, err
	}
	return list[0], audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionStatusChange, EntityType: "golf.bag_storage", EntityID: sid.String(),
		PropertyID: &property, After: map[string]any{"status": "ended"}})
}
