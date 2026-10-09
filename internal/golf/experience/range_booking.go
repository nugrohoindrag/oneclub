package experience

// Driving Range booking (demo feedback 9 Oct 2026; PRD P2 FR-RNG-02 "booking
// opsional lewat EP-01"). A player books from the Member App, the website or
// the front desk, either a bay and a time — the bay is held in the
// Reservation Engine — or only the visit (balls are bought at the Driving
// Range Counter). The time is flexible: at check-in the guest gets the
// booked bay when it is free, otherwise another free bay or the queue; a
// guest more than RangeGraceMinutes late loses the bay hold and queues.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/crm"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/reservation"
)

// Range booking grid (club time); the opening hours come from the Golf Policy.
const (
	RangeStepMinutes  = 30
	RangeGraceMinutes = 15
)

// rangeHours returns the opening hours of the driving range on a day: the
// Golf Policy in force at the start of the day, or now for today (a version
// saved during the day applies at once).
func rangeHours(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (open, close time.Time, err error) {
	at := day
	if now := clock.Now(); now.After(at) {
		at = now
	}
	pol, err := golf.LoadPolicies(ctx, q, property, at)
	if err != nil {
		return open, close, err
	}
	return day.Add(time.Duration(pol.Golf.RangeOpenHour) * time.Hour), day.Add(time.Duration(pol.Golf.RangeCloseHour) * time.Hour), nil
}

// RangeBooking is a booked bay time or an announced range visit.
type RangeBooking struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	Number        string     `json:"number" db:"number"`
	PlayDate      string     `json:"playDate" db:"play_date"`
	StartAt       time.Time  `json:"startAt" db:"start_at"`
	EndAt         time.Time  `json:"endAt" db:"end_at"`
	Area          string     `json:"area" db:"area" enum:"indoor,outdoor"`
	BayID         *uuid.UUID `json:"bayId" db:"bay_id"`
	BayCode       *string    `json:"bayCode" db:"bay_code"`
	HoldsBay      bool       `json:"holdsBay" db:"holds_bay" doc:"The bay is held for the time (released when the guest is late or leaves)"`
	Players       int        `json:"players" db:"players"`
	CustomerID    *uuid.UUID `json:"customerId" db:"customer_id"`
	GuestName     string     `json:"guestName" db:"guest_name"`
	GuestPhone    *string    `json:"guestPhone" db:"guest_phone"`
	Channel       string     `json:"channel" db:"channel" enum:"member_app,website,back_office,walk_in"`
	Status        string     `json:"status" db:"status" enum:"booked,checked_in,cancelled,no_show"`
	SessionID     *uuid.UUID `json:"sessionId" db:"session_id"`
	SessionStatus *string    `json:"sessionStatus" db:"session_status" doc:"Range session after check-in: waiting (queue), active (on a bay), finished"`
	BayNow        *string    `json:"bayNow" db:"bay_now" doc:"The bay the guest plays on after check-in"`
	Notes         *string    `json:"notes" db:"notes"`
	CreatedAt     time.Time  `json:"createdAt" db:"created_at"`
}

const rangeBookingSelect = `SELECT r.id, r.number, to_char(r.play_date, 'YYYY-MM-DD') AS play_date, r.start_at, r.end_at, r.area, r.bay_id, b.code AS bay_code,
	(r.reservation_id IS NOT NULL AND r.bay_released_at IS NULL AND r.status IN ('booked', 'checked_in')) AS holds_bay, r.players, r.customer_id,
	r.guest_name, r.guest_phone, r.channel, r.status, r.session_id, s.status AS session_status, sb.code AS bay_now, r.notes, r.created_at
	FROM golf.range_bookings r LEFT JOIN golf.range_bays b ON b.id = r.bay_id LEFT JOIN golf.range_sessions s ON s.id = r.session_id
	LEFT JOIN golf.range_bays sb ON sb.id = s.bay_id`

func (m *Module) rangeBooking(ctx context.Context, q dbtx.Querier, property, bid uuid.UUID) (RangeBooking, error) {
	rows, err := q.Query(ctx, rangeBookingSelect+` WHERE r.id = $1 AND r.property_id = $2`, bid, property)
	return handle.One[RangeBooking](rows, err, "range booking")
}

// RangeBookingInput books the range.
type RangeBookingInput struct {
	Date       string     `json:"date" doc:"Play date (YYYY-MM-DD)"`
	Time       string     `json:"time" doc:"Arrival time HH:MM (club time); flexible — a late guest queues"`
	Minutes    int        `json:"minutes,omitempty" doc:"Bay time 30–240 minutes (default 60)"`
	Area       string     `json:"area,omitempty" enum:"indoor,outdoor"`
	ReserveBay bool       `json:"reserveBay,omitempty" doc:"Hold a bay for the time; false: only announce the visit (balls at the counter)"`
	BayID      *uuid.UUID `json:"bayId,omitempty" doc:"A chosen bay (with reserveBay); default the first free bay"`
	Players    int        `json:"players,omitempty"`
	CustomerID *uuid.UUID `json:"customerId,omitempty" doc:"Front desk: the customer or member (empty for a walk-in guest)"`
	GuestName  string     `json:"guestName,omitempty"`
	GuestPhone string     `json:"guestPhone,omitempty"`
	GuestEmail string     `json:"guestEmail,omitempty"`
	Notes      string     `json:"notes,omitempty"`
}

// RangeSlot is a start time with the bays free for the requested length.
// A busy time never refuses a booking (first come first served): the guest
// books the visit and queues for the next free bay.
type RangeSlot struct {
	Time     string        `json:"time"`
	StartAt  time.Time     `json:"startAt"`
	FreeBays int           `json:"freeBays"`
	Bays     []RangeBayRef `json:"bays"`
	Crowd    string        `json:"crowd" enum:"quiet,peak" doc:"peak (red): every bay is taken by bookings at this time — expect to queue; quiet (green)"`
}

// RangeBayRef is a free bay.
type RangeBayRef struct {
	ID   uuid.UUID `json:"id"`
	Code string    `json:"code"`
	Name string    `json:"name"`
}

type rangeBay struct {
	ID         uuid.UUID  `db:"id"`
	Code       string     `db:"code"`
	Name       string     `db:"name"`
	Area       string     `db:"area"`
	ResourceID *uuid.UUID `db:"resource_id"`
}

func (m *Module) rangeBays(ctx context.Context, q dbtx.Querier, property uuid.UUID, area string) ([]rangeBay, error) {
	return handle.List[rangeBay](q.Query(ctx, `SELECT id, code, name, area, resource_id FROM golf.range_bays WHERE property_id = $1 AND area = $2
		AND status = 'active' AND archived_at IS NULL AND resource_id IS NOT NULL ORDER BY tier, code`, property, area))
}

func (m *Module) freeBays(ctx context.Context, q dbtx.Querier, bays []rangeBay, start, end time.Time) ([]rangeBay, error) {
	res := make([]uuid.UUID, 0, len(bays))
	for _, b := range bays {
		res = append(res, *b.ResourceID)
	}
	busy, err := reservation.Busy(ctx, q, res, start, end)
	if err != nil {
		return nil, err
	}
	var out []rangeBay
	for _, b := range bays {
		if !busy[*b.ResourceID] {
			out = append(out, b)
		}
	}
	return out, nil
}

func rangeMinutes(n int) (int, error) {
	if n == 0 {
		return 60, nil
	}
	if n < 30 || n > 240 {
		return 0, handle.Invalid("minutes", "invalid", "30 – 240 minutes")
	}
	return n, nil
}

// RangeAvailability lists the start times of a day with the free bays.
func (m *Module) RangeAvailability(ctx context.Context, q dbtx.Querier, property uuid.UUID, date, area string, minutes int) ([]RangeSlot, error) {
	var err error
	if minutes, err = rangeMinutes(minutes); err != nil {
		return nil, err
	}
	if area, err = m.bookableArea(ctx, q, property, area); err != nil {
		return nil, err
	}
	loc := location(ctx, q, property)
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, handle.Invalid("date", "invalid", "YYYY-MM-DD")
	}
	bays, err := m.rangeBays(ctx, q, property, area)
	if err != nil {
		return nil, err
	}
	// announced visits without a bay also fill the range
	type visit struct {
		Start time.Time `db:"start_at"`
		End   time.Time `db:"end_at"`
	}
	visits, err := handle.List[visit](q.Query(ctx, `SELECT start_at, end_at FROM golf.range_bookings WHERE property_id = $1 AND area = $2
		AND play_date = $3::date AND status = 'booked' AND (reservation_id IS NULL OR bay_released_at IS NOT NULL)`, property, area, date))
	if err != nil {
		return nil, err
	}
	opens, closes, err := rangeHours(ctx, q, property, day)
	if err != nil {
		return nil, err
	}
	now := clock.Now()
	out := []RangeSlot{}
	length := time.Duration(minutes) * time.Minute
	for t := opens; !t.Add(length).After(closes); t = t.Add(RangeStepMinutes * time.Minute) {
		if t.Add(length).Before(now) {
			continue
		}
		free, err := m.freeBays(ctx, q, bays, t, t.Add(length))
		if err != nil {
			return nil, err
		}
		taken := len(bays) - len(free)
		for _, v := range visits {
			if v.Start.Before(t.Add(length)) && v.End.After(t) {
				taken++
			}
		}
		crowd := "quiet"
		if taken >= len(bays) {
			crowd = "peak"
		}
		s := RangeSlot{Time: t.Format("15:04"), StartAt: t, FreeBays: len(free), Bays: []RangeBayRef{}, Crowd: crowd}
		for _, b := range free {
			s.Bays = append(s.Bays, RangeBayRef{ID: b.ID, Code: b.Code, Name: b.Name})
		}
		out = append(out, s)
	}
	return out, nil
}

// BookRange books a bay time or a visit.
func (m *Module) BookRange(ctx context.Context, tx pgx.Tx, property uuid.UUID, in RangeBookingInput, channel string) (RangeBooking, error) {
	minutes, err := rangeMinutes(in.Minutes)
	if err != nil {
		return RangeBooking{}, err
	}
	area, err := m.bookableArea(ctx, tx, property, in.Area)
	if err != nil {
		return RangeBooking{}, err
	}
	loc := location(ctx, tx, property)
	start, err := time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(in.Date)+" "+strings.TrimSpace(in.Time), loc)
	if err != nil {
		return RangeBooking{}, handle.Invalid("time", "invalid", "date YYYY-MM-DD and time HH:MM")
	}
	end := start.Add(time.Duration(minutes) * time.Minute)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	opens, closes, err := rangeHours(ctx, tx, property, day)
	if err != nil {
		return RangeBooking{}, err
	}
	if start.Before(opens) || end.After(closes) {
		return RangeBooking{}, handle.Invalid("time", "closed", fmt.Sprintf("the range is open %02.0f:00 – %02.0f:00", opens.Sub(day).Hours(), closes.Sub(day).Hours()))
	}
	if !end.After(clock.Now()) {
		return RangeBooking{}, handle.Invalid("time", "in_the_past", "choose a time that has not passed")
	}
	players := in.Players
	if players == 0 {
		players = 1
	}
	if players < 1 || players > 8 {
		return RangeBooking{}, handle.Invalid("players", "invalid", "1 – 8 players")
	}
	name := strings.TrimSpace(in.GuestName)
	if in.CustomerID != nil && name == "" {
		c, err := crm.GetCustomer(ctx, tx, *in.CustomerID)
		if err != nil {
			return RangeBooking{}, err
		}
		name = c.Name
		if in.GuestPhone == "" {
			in.GuestPhone = c.Phone
		}
	}
	if name == "" {
		return RangeBooking{}, handle.Invalid("guestName", "required", "name is required")
	}
	bid := id.New()
	no, err := number(ctx, tx, property, "DR")
	if err != nil {
		return RangeBooking{}, err
	}
	var bayID, rid *uuid.UUID
	if in.ReserveBay || in.BayID != nil {
		bays, err := m.rangeBays(ctx, tx, property, area)
		if err != nil {
			return RangeBooking{}, err
		}
		free, err := m.freeBays(ctx, tx, bays, start, end)
		if err != nil {
			return RangeBooking{}, err
		}
		var pick *rangeBay
		for i := range free {
			if in.BayID == nil || free[i].ID == *in.BayID {
				pick = &free[i]
				break
			}
		}
		if pick != nil {
			r, err := m.Reservations.Book(ctx, tx, property, reservation.BookRequest{Lines: []reservation.LineRequest{{ResourceID: *pick.ResourceID, Start: start, End: end,
				Description: "Driving range " + pick.Code}}, BusinessLine: "golf", CustomerID: in.CustomerID, GuestName: name, GuestPhone: in.GuestPhone,
				GuestEmail: in.GuestEmail, Channel: channel, SourceType: "golf.range_booking", SourceID: &bid, Confirm: true, Notes: in.Notes})
			if err != nil {
				return RangeBooking{}, err
			}
			bayID, rid = &pick.ID, &r.ID
		}
		// every bay is taken: the booking stands without a bay hold and the
		// guest queues for the next free bay (FIFO, never refused)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.range_bookings (id, property_id, number, play_date, start_at, end_at, area, bay_id, reservation_id, players,
		customer_id, guest_name, guest_phone, guest_email, channel, notes, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		bid, property, no, day.Format("2006-01-02"), start, end, area, bayID, rid, players, in.CustomerID, name, nullStr(in.GuestPhone),
		nullStr(strings.ToLower(strings.TrimSpace(in.GuestEmail))), channel, nullStr(in.Notes), actorPtr(ctx)); err != nil {
		return RangeBooking{}, err
	}
	b, err := m.rangeBooking(ctx, tx, property, bid)
	if err != nil {
		return b, err
	}
	return b, record(ctx, tx, "golf.range_booking", bid, no, audit.ActionCreate, property, nil, b, "")
}

// releaseBayHold ends the bay hold now (late guest, other bay, session over).
func (m *Module) releaseBayHold(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	r, err := m.Reservations.Get(ctx, tx, rid, false)
	if err != nil {
		return err
	}
	now := clock.Now()
	if r.Status == reservation.StatusConfirmed && len(r.Lines) > 0 && r.Lines[0].Start.After(now) {
		// early: nothing of the hold was used
		_, err := m.Reservations.Cancel(ctx, tx, rid, "range guest checked in early", true)
		return err
	}
	for _, l := range r.Lines {
		if (l.Status == "confirmed" || l.Status == "checked_in") && l.End.After(now) {
			if err := m.Reservations.ShortenLine(ctx, tx, l.ID, now); err != nil {
				return err
			}
		}
	}
	switch r.Status {
	case reservation.StatusConfirmed, reservation.StatusCheckedIn:
		_, err = m.Reservations.Complete(ctx, tx, rid)
	}
	return err
}

// CheckInRange starts the range session: the booked bay when it is free,
// otherwise the first free bay or the queue (the time is flexible).
func (m *Module) CheckInRange(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID) (RangeBooking, error) {
	b, err := m.rangeBooking(ctx, tx, property, bid)
	if err != nil {
		return b, err
	}
	if b.Status != "booked" {
		return b, errs.Conflict("invalid_status", "the range booking is "+b.Status)
	}
	var rid *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT reservation_id FROM golf.range_bookings WHERE id = $1 FOR UPDATE`, bid).Scan(&rid); err != nil {
		return b, err
	}
	var bay *uuid.UUID
	if b.HoldsBay && b.BayID != nil {
		var readiness string
		if err := tx.QueryRow(ctx, `SELECT readiness FROM golf.range_bays WHERE id = $1`, *b.BayID).Scan(&readiness); err != nil {
			return b, err
		}
		if readiness == "available" {
			bay = b.BayID
		}
	}
	if b.HoldsBay && rid != nil {
		// the hold is replaced by the bay itself (or freed for others)
		if err := m.releaseBayHold(ctx, tx, *rid); err != nil {
			return b, err
		}
	}
	s, err := m.StartSession(ctx, tx, property, RangeSessionInput{CustomerID: b.CustomerID, GuestName: b.GuestName, Area: b.Area, BayID: bay})
	if err != nil {
		return b, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.range_bookings SET status = 'checked_in', checked_in_at = now(), session_id = $2,
		bay_released_at = coalesce(bay_released_at, CASE WHEN reservation_id IS NOT NULL THEN now() END) WHERE id = $1`, bid, s.ID); err != nil {
		return b, err
	}
	after, err := m.rangeBooking(ctx, tx, property, bid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.range_booking", bid, b.Number, "check_in", property, b, after, "")
}

// CancelRange cancels a booking that has not checked in; the bay hold is
// freed without a fee.
func (m *Module) CancelRange(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID, reason string) (RangeBooking, error) {
	b, err := m.rangeBooking(ctx, tx, property, bid)
	if err != nil {
		return b, err
	}
	if b.Status != "booked" {
		return b, errs.Conflict("invalid_status", "the range booking is "+b.Status)
	}
	var rid *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT reservation_id FROM golf.range_bookings WHERE id = $1 FOR UPDATE`, bid).Scan(&rid); err != nil {
		return b, err
	}
	if rid != nil && b.HoldsBay {
		if _, err := m.Reservations.Cancel(ctx, tx, *rid, reason, true); err != nil {
			return b, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.range_bookings SET status = 'cancelled', cancelled_at = now(), bay_released_at = coalesce(bay_released_at, now())
		WHERE id = $1`, bid); err != nil {
		return b, err
	}
	after, err := m.rangeBooking(ctx, tx, property, bid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.range_booking", bid, b.Number, audit.ActionStatusChange, property, b, after, reason)
}

// ReleaseLateRangeBays frees the bays of guests more than the grace period
// late (they queue when they arrive) and marks visits that never came.
func (m *Module) ReleaseLateRangeBays(ctx context.Context, tx pgx.Tx) (int, error) {
	now := clock.Now()
	rows, err := tx.Query(ctx, `SELECT id, reservation_id FROM golf.range_bookings WHERE status = 'booked' AND reservation_id IS NOT NULL
		AND bay_released_at IS NULL AND start_at < $1 FOR UPDATE SKIP LOCKED`, now.Add(-RangeGraceMinutes*time.Minute))
	if err != nil {
		return 0, err
	}
	type late struct {
		ID  uuid.UUID
		RID uuid.UUID
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[late])
	if err != nil {
		return 0, err
	}
	for _, l := range list {
		if err := m.releaseBayHold(ctx, tx, l.RID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.range_bookings SET bay_released_at = now() WHERE id = $1`, l.ID); err != nil {
			return 0, err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE golf.range_bookings SET status = 'no_show' WHERE status = 'booked' AND end_at < $1`, now)
	if err != nil {
		return 0, err
	}
	return len(list) + int(tag.RowsAffected()), nil
}

// RangeHoldArgs runs the late release every five minutes.
type RangeHoldArgs struct{}

func (RangeHoldArgs) Kind() string { return "golf_range_late_release" }

func (RangeHoldArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 1}
}

type RangeHoldWorker struct {
	river.WorkerDefaults[RangeHoldArgs]
	M *Module
}

func (w *RangeHoldWorker) Work(ctx context.Context, _ *river.Job[RangeHoldArgs]) error {
	sys := dbtx.System(ctx)
	return w.M.DB.WithTx(sys, func(tx pgx.Tx) error {
		_, err := w.M.ReleaseLateRangeBays(sys, tx)
		return err
	})
}

// ── HTTP ───────────────────────────────────────────────────────────────────

// RangeCancelInput cancels a range booking.
type RangeCancelInput struct {
	Reason string `json:"reason,omitempty"`
}

// PublicRangeBookingInput books the range from the website (no account).
type PublicRangeBookingInput struct {
	PropertyID uuid.UUID       `json:"propertyId"`
	Guest      crm.PublicGuest `json:"guest"`
	RangeBookingInput
}

func (in PublicRangeBookingInput) Property() uuid.UUID      { return in.PropertyID }
func (in PublicRangeBookingInput) Visitor() crm.PublicGuest { return in.Guest }

func queryMinutes(r *http.Request) int { return handle.QueryInt(r, "minutes", 60) }

func (m *Module) registerRangeBookings(reg *route.Registry, add func(tag string, rt route.Route)) {
	db := m.DB
	add("Driving Range", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/range-availability", Summary: "Driving range start times with free bays",
		Permission: "golf.range.view", Response: RangeSlot{}, List: true, Query: []route.Param{{Name: "date", Required: true}, {Name: "area"}, {Name: "minutes", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RangeSlot], error) {
			q := r.URL.Query()
			out, err := m.RangeAvailability(ctx, tx, handle.Property(ctx), q.Get("date"), q.Get("area"), queryMinutes(r))
			return httpx.Page[RangeSlot]{Items: out}, err
		})})
	add("Driving Range", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/range-bookings", Summary: "Driving range bookings of a day",
		Permission: "golf.range.view", Response: RangeBooking{}, List: true, Query: []route.Param{{Name: "date"}, {Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RangeBooking], error) {
			day := r.URL.Query().Get("date")
			if day == "" {
				day = localNow(ctx, tx).Format("2006-01-02")
			}
			return handle.Page(handle.List[RangeBooking](tx.Query(ctx, rangeBookingSelect+` WHERE r.property_id = $1 AND r.play_date = $2::date
				AND ($3 = '' OR r.status = $3) ORDER BY r.start_at, r.number`, handle.Property(ctx), day, r.URL.Query().Get("filter[status]"))))
		})})
	add("Driving Range", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/range-bookings", Summary: "Book the range at the front desk (bay and time, or the visit)",
		Permission: "golf.range.operate", Request: RangeBookingInput{}, Response: RangeBooking{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RangeBookingInput) (RangeBooking, error) {
			channel := "back_office"
			if in.CustomerID == nil {
				channel = "walk_in"
			}
			return m.BookRange(ctx, tx, handle.Property(ctx), in, channel)
		})})
	add("Driving Range", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/range-bookings/{id}:check-in", Summary: "Check in: the booked bay when free, else another bay or the queue",
		Permission: "golf.range.operate", Response: RangeBooking{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (RangeBooking, error) {
			bid, err := handle.ID(r)
			if err != nil {
				return RangeBooking{}, err
			}
			return m.CheckInRange(ctx, tx, handle.Property(ctx), bid)
		})})
	add("Driving Range", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/range-bookings/{id}:cancel", Summary: "Cancel a range booking (no fee)",
		Permission: "golf.range.operate", Request: RangeCancelInput{}, Response: RangeBooking{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RangeCancelInput) (RangeBooking, error) {
			bid, err := handle.ID(r)
			if err != nil {
				return RangeBooking{}, err
			}
			return m.CancelRange(ctx, tx, handle.Property(ctx), bid, in.Reason)
		})})

	// Member App
	me := func(rt route.Route) { crm.MeRoute(reg, "golf", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/range-availability", Summary: "Driving range start times with free bays",
		Response: RangeSlot{}, List: true, Query: []route.Param{{Name: "date", Required: true}, {Name: "area"}, {Name: "minutes", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RangeSlot], error) {
			q := r.URL.Query()
			out, err := m.RangeAvailability(ctx, tx, handle.Property(ctx), q.Get("date"), q.Get("area"), queryMinutes(r))
			return httpx.Page[RangeSlot]{Items: out}, err
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/range-bookings", Summary: "My driving range bookings", Response: RangeBooking{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RangeBooking], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[RangeBooking]{}, err
			}
			return handle.Page(handle.List[RangeBooking](tx.Query(ctx, rangeBookingSelect+` WHERE r.customer_id = $1 ORDER BY r.start_at DESC LIMIT 50`, c.ID)))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/range-bookings", Summary: "Book the driving range (bay and time, or the visit)",
		Request: RangeBookingInput{}, Response: RangeBooking{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RangeBookingInput) (RangeBooking, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return RangeBooking{}, err
			}
			in.CustomerID, in.GuestName, in.GuestPhone, in.GuestEmail = &c.ID, c.Name, c.Phone, c.Email
			return m.BookRange(ctx, tx, handle.Property(ctx), in, "member_app")
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/range-bookings/{id}:cancel", Summary: "Cancel my driving range booking",
		Request: RangeCancelInput{}, Response: RangeBooking{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RangeCancelInput) (RangeBooking, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return RangeBooking{}, err
			}
			bid, err := handle.ID(r)
			if err != nil {
				return RangeBooking{}, err
			}
			b, err := m.rangeBooking(ctx, tx, handle.Property(ctx), bid)
			if err != nil || b.CustomerID == nil || *b.CustomerID != c.ID {
				return RangeBooking{}, errs.NotFound("range booking")
			}
			reason := in.Reason
			if reason == "" {
				reason = "cancelled by the member"
			}
			return m.CancelRange(ctx, tx, handle.Property(ctx), bid, reason)
		})})

	// website: no account needed
	crm.PublicRoute(reg, "golf", route.Route{Method: http.MethodGet, Path: "/api/v1/public/golf/range-availability", Summary: "Driving range start times with free bays",
		Response: RangeSlot{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "date", Required: true}, {Name: "area"},
			{Name: "minutes", Type: "integer"}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out []RangeSlot
			err = db.WithReadTx(ctx, func(tx pgx.Tx) error {
				q := r.URL.Query()
				out, err = m.RangeAvailability(ctx, tx, pid, q.Get("date"), q.Get("area"), queryMinutes(r))
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, httpx.Page[RangeSlot]{Items: out})
		}})
	crm.PublicRoute(reg, "golf", route.Route{Method: http.MethodPost, Path: "/api/v1/public/golf/range-bookings", Summary: "Book the driving range from the website (no account)",
		Request: PublicRangeBookingInput{}, Response: RangeBooking{}, Status: http.StatusCreated,
		Handler: crm.PublicWrite(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c crm.Customer, in PublicRangeBookingInput) (RangeBooking, error) {
			rb := in.RangeBookingInput
			rb.CustomerID, rb.GuestName, rb.GuestPhone, rb.GuestEmail = &c.ID, in.Guest.Name, in.Guest.Phone, in.Guest.Email
			return m.BookRange(ctx, tx, pid, rb, "website")
		})})
}
