package golf

// EP-03 Tee Time & Tee Sheet and EP-02 course availability blocking.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/ratelimit"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/membership"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/notification"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/realtime"
)

// Publisher publishes domain events.
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// Module is the golf module.
type Module struct {
	DB           *dbtx.DB
	Events       Publisher
	Approvals    *approval.Engine
	Billing      *billing.Service
	Refunds      billing.Approver
	Notify       notify.Sender
	Hub          *realtime.Hub
	Integrations *integration.Service
	Cfg          *config.Config
	Limiter      *ratelimit.Limiter
}

func prop(ctx context.Context) uuid.UUID {
	p, _ := reqctx.Property(ctx)
	return p
}

func actor(ctx context.Context) uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return p.UserID
	}
	return uuid.Nil
}

func authzFrom(ctx context.Context) *authz.Principal { return authz.From(ctx) }

func withProperty(ctx context.Context, p uuid.UUID) context.Context {
	return reqctx.WithProperty(ctx, p)
}

func actorName(ctx context.Context) string {
	if p := authz.From(ctx); p != nil {
		return p.Name
	}
	return "system"
}

func location(ctx context.Context, q dbtx.Querier, property uuid.UUID) *time.Location {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}

// localDay returns the local business date (as a UTC midnight date value).
func localDay(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

func atLocal(day time.Time, hhmm string, loc *time.Location) time.Time {
	t, _ := time.Parse("15:04", hhmm)
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, loc)
}

func parseDate(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return t, errs.Validation("invalid_date", "date must be YYYY-MM-DD", errs.Field("date", "invalid", "YYYY-MM-DD"))
	}
	return t, nil
}

// notifyRealtime pushes a tee sheet change to every open screen (< 2 s,
// FR-TEE-09) when the transaction commits.
func notifyRealtime(ctx context.Context, tx pgx.Tx, property uuid.UUID, topic, typ string, courseID uuid.UUID, day time.Time, extra map[string]any) error {
	data := map[string]any{"courseId": courseID.String(), "date": day.Format("2006-01-02")}
	for k, v := range extra {
		data[k] = v
	}
	return realtime.Publish(ctx, tx, topic, typ, &property, data)
}

// ── generation (FR-TEE-03) ────────────────────────────────────────────────

type template struct {
	ID              uuid.UUID
	CourseID        uuid.UUID
	Session         string
	Start, End      string
	Interval        int
	Tees            []int
	Flights         int
	MinP, MaxP      int
	RouteID         *uuid.UUID
	Peak, MemberOnl bool
	Lighting        bool
	DayType         string
}

// GenerateResult reports a generation run.
type GenerateResult struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Closed    int `json:"closed"`
	Unchanged int `json:"unchanged"`
	Days      int `json:"days"`
}

// SeatCode is the reservation resource code of one tee time seat.
func SeatCode(courseCode string, tee, flight, seat int) string {
	return fmt.Sprintf("GOLF-%s-T%d-F%d-S%d", courseCode, tee, flight, seat)
}

// Generate creates / refreshes the slots of the given local days. Slots
// with an active booking are never changed (FR-TEE-11).
func (m *Module) Generate(ctx context.Context, tx pgx.Tx, property uuid.UUID, courseID *uuid.UUID, from time.Time, days int) (GenerateResult, error) {
	var res GenerateResult
	loc := location(ctx, tx, property)
	type course struct {
		id      uuid.UUID
		code    string
		venueID uuid.UUID
	}
	var courses []course
	rows, err := tx.Query(ctx, `SELECT id, code, venue_id FROM golf.courses WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND ($2::uuid IS NULL OR id = $2)`, property, courseID)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var c course
		if err := rows.Scan(&c.id, &c.code, &c.venueID); err != nil {
			rows.Close()
			return res, err
		}
		courses = append(courses, c)
	}
	rows.Close()
	for d := 0; d < days; d++ {
		day := from.AddDate(0, 0, d)
		dayType, err := commercial.DayTypeFor(ctx, tx, property, day)
		if err != nil {
			if errs.Is(err, errs.KindConflict) {
				continue // no day type configured for this date
			}
			return res, err
		}
		res.Days++
		for _, c := range courses {
			tpls, err := templatesFor(ctx, tx, c.id, dayType, day)
			if err != nil {
				return res, err
			}
			type key struct {
				at  time.Time
				tee int
			}
			desired := map[key]template{}
			for _, t := range tpls {
				first, last := atLocal(day, t.Start, loc), atLocal(day, t.End, loc)
				for at := first; !at.After(last); at = at.Add(time.Duration(t.Interval) * time.Minute) {
					for _, tee := range t.Tees {
						desired[key{at.UTC(), tee}] = t
					}
				}
				for _, tee := range t.Tees {
					for f := 1; f <= t.Flights; f++ {
						for s := 1; s <= t.MaxP; s++ {
							if _, err := reservationEnsureSeat(ctx, tx, property, c.code, c.venueID, tee, f, s); err != nil {
								return res, err
							}
						}
					}
				}
			}
			type existing struct {
				id     uuid.UUID
				status string
				tpl    *uuid.UUID
				cap    int
				booked bool
			}
			ex := map[key]existing{}
			er, err := tx.Query(ctx, `SELECT t.id, t.start_at, t.start_tee, t.status, t.template_id, t.capacity,
				EXISTS (SELECT 1 FROM golf.bookings b WHERE b.tee_time_id = t.id AND b.status NOT IN ('cancelled'))
				  OR EXISTS (SELECT 1 FROM golf.flights f WHERE f.tee_time_id = t.id AND f.status <> 'cancelled')
				FROM golf.tee_times t WHERE t.course_id = $1 AND t.play_date = $2::date`, c.id, day.Format("2006-01-02"))
			if err != nil {
				return res, err
			}
			for er.Next() {
				var e existing
				var at time.Time
				var tee int
				if err := er.Scan(&e.id, &at, &tee, &e.status, &e.tpl, &e.cap, &e.booked); err != nil {
					er.Close()
					return res, err
				}
				ex[key{at.UTC(), tee}] = e
			}
			er.Close()
			for k, e := range ex {
				t, want := desired[k]
				switch {
				case e.booked:
					res.Unchanged++
				case !want && e.status != "closed":
					if _, err := tx.Exec(ctx, `UPDATE golf.tee_times SET status = 'closed' WHERE id = $1`, e.id); err != nil {
						return res, err
					}
					res.Closed++
				case want && (e.tpl == nil || *e.tpl != t.ID || e.status == "closed" || e.cap != t.Flights*t.MaxP):
					if _, err := tx.Exec(ctx, `UPDATE golf.tee_times SET template_id = $2, session = $3, day_type_code = $4, interval_minutes = $5,
						flights = $6, min_players = $7, max_players = $8, capacity = $9, playing_route_id = $10, peak = $11, member_only = $12, lighting = $13,
						status = CASE WHEN status = 'blocked' THEN 'blocked' ELSE 'open' END WHERE id = $1`,
						e.id, t.ID, t.Session, dayType, t.Interval, t.Flights, t.MinP, t.MaxP, t.Flights*t.MaxP, t.RouteID, t.Peak, t.MemberOnl, t.Lighting); err != nil {
						return res, err
					}
					res.Updated++
				default:
					res.Unchanged++
				}
			}
			for k, t := range desired {
				if _, ok := ex[k]; ok {
					continue
				}
				var blockID *uuid.UUID
				_ = tx.QueryRow(ctx, `SELECT id FROM golf.course_blocks WHERE course_id = $1 AND status = 'active' AND playing_route_id IS NULL
					AND starts_at <= $2 AND ends_at > $2 LIMIT 1`, c.id, k.at).Scan(&blockID)
				status := "open"
				if blockID != nil {
					status = "blocked"
				}
				if _, err := tx.Exec(ctx, `INSERT INTO golf.tee_times (id, property_id, course_id, play_date, start_at, start_tee, session, day_type_code,
					template_id, interval_minutes, flights, min_players, max_players, capacity, playing_route_id, peak, member_only, lighting, status, block_id)
					VALUES ($1,$2,$3,$4::date,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) ON CONFLICT (course_id, start_at, start_tee) DO NOTHING`,
					id.New(), property, c.id, day.Format("2006-01-02"), k.at, k.tee, t.Session, dayType, t.ID, t.Interval, t.Flights, t.MinP, t.MaxP,
					t.Flights*t.MaxP, t.RouteID, t.Peak, t.MemberOnl, t.Lighting, status, blockID); err != nil {
					return res, err
				}
				res.Created++
			}
		}
	}
	return res, nil
}

func templatesFor(ctx context.Context, q dbtx.Querier, courseID uuid.UUID, dayType string, day time.Time) ([]template, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (session) id, course_id, session, start_time, end_time, interval_minutes, start_tees, flights_per_slot,
		min_players, max_players, playing_route_id, peak, member_only, lighting, day_type_code
		FROM golf.tee_sheet_templates WHERE course_id = $1 AND upper(day_type_code) = upper($2) AND status = 'active' AND archived_at IS NULL
		  AND effective_from <= $3::date AND (effective_to IS NULL OR effective_to >= $3::date)
		ORDER BY session, effective_from DESC, created_at DESC`, courseID, dayType, day.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []template
	for rows.Next() {
		var t template
		var tees string
		if err := rows.Scan(&t.ID, &t.CourseID, &t.Session, &t.Start, &t.End, &t.Interval, &tees, &t.Flights, &t.MinP, &t.MaxP, &t.RouteID, &t.Peak,
			&t.MemberOnl, &t.Lighting, &t.DayType); err != nil {
			return nil, err
		}
		t.Tees = []int{1}
		if tees == "1,10" {
			t.Tees = []int{1, 10}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GenerateRequest triggers a generation.
type GenerateRequest struct {
	CourseID *uuid.UUID `json:"courseId,omitempty"`
	From     route.Date `json:"from,omitempty" doc:"Default today"`
	Days     int        `json:"days,omitempty" doc:"Default: the longest booking window + 1"`
}

func (m *Module) maxWindow(ctx context.Context, q dbtx.Querier, property uuid.UUID) int {
	pol, err := LoadPolicies(ctx, q, property, clock.Now())
	if err != nil {
		return 15
	}
	mx := 0
	for _, v := range pol.Golf.BookingWindowDays {
		if v > mx {
			mx = v
		}
	}
	typeMax, _ := membership.MaxBookingWindowDays(ctx, q, property)
	if typeMax != nil && *typeMax > mx {
		mx = *typeMax
	}
	return mx + 1
}

func (m *Module) generateHTTP(w http.ResponseWriter, r *http.Request) {
	var req GenerateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	p := prop(ctx)
	var out GenerateResult
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		from := localDay(clock.Now(), location(ctx, tx, p))
		if req.From != "" {
			d, err := parseDate(string(req.From))
			if err != nil {
				return err
			}
			from = d
		}
		days := req.Days
		if days <= 0 {
			days = m.maxWindow(ctx, tx, p)
		}
		if days > 120 {
			return errs.Validation("too_many_days", "generate at most 120 days at once", errs.Field("days", "too_large", "≤ 120"))
		}
		var err error
		out, err = m.Generate(ctx, tx, p, req.CourseID, from, days)
		if err != nil {
			return err
		}
		if req.CourseID != nil {
			if err := notifyRealtime(ctx, tx, p, "golf.tee_sheet", "generated", *req.CourseID, from, nil); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "generate_tee_sheet", EntityType: "golf.tee_time", EntityLabel: from.Format("2006-01-02"),
			PropertyID: &p, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ── slots, availability, tee sheet view ───────────────────────────────────

// Slot is one tee time with its capacity.
type Slot struct {
	ID             uuid.UUID  `json:"id"`
	CourseID       uuid.UUID  `json:"courseId"`
	PlayDate       string     `json:"playDate"`
	StartAt        time.Time  `json:"startAt"`
	LocalTime      string     `json:"localTime"`
	StartTee       int        `json:"startTee"`
	Session        string     `json:"session" enum:"morning,afternoon,night"`
	DayTypeCode    string     `json:"dayTypeCode"`
	Capacity       int        `json:"capacity"`
	Used           int        `json:"used"`
	Remaining      int        `json:"remaining"`
	MinPlayers     int        `json:"minPlayers"`
	MaxPlayers     int        `json:"maxPlayers"`
	Flights        int        `json:"flightCapacity"`
	PlayingRouteID *uuid.UUID `json:"playingRouteId"`
	Peak           bool       `json:"peak"`
	MemberOnly     bool       `json:"memberOnly"`
	Lighting       bool       `json:"lighting"`
	Status         string     `json:"status" enum:"available,reserved,full,blocked"`
	BlockReason    *string    `json:"blockReason"`
}

func slotStatus(raw string, used, capacity int) string {
	switch {
	case raw == "blocked":
		return "blocked"
	case used >= capacity:
		return "full"
	case used > 0:
		return "reserved"
	}
	return "available"
}

// slotUsage counts seats in use per tee time: players of active bookings
// plus seats held by unexpired draft holds.
const slotUsage = `(SELECT coalesce(sum(CASE WHEN b.status = 'draft' THEN CASE WHEN b.hold_expires_at > now() THEN b.player_count ELSE 0 END ELSE 0 END), 0)
	FROM golf.bookings b WHERE b.tee_time_id = t.id AND b.status = 'draft')
	+ (SELECT count(*) FROM golf.booking_players bp JOIN golf.flights f ON f.id = bp.flight_id JOIN golf.bookings b ON b.id = bp.booking_id
	   WHERE f.tee_time_id = t.id AND bp.status IN ('booked', 'checked_in') AND b.status IN ('pending', 'confirmed', 'checked_in', 'completed'))`

func loadSlots(ctx context.Context, q dbtx.Querier, loc *time.Location, where string, args ...any) ([]Slot, error) {
	rows, err := q.Query(ctx, `SELECT t.id, t.course_id, t.play_date, t.start_at, t.start_tee, t.session, t.day_type_code, t.capacity, `+slotUsage+`,
		t.min_players, t.max_players, t.flights, t.playing_route_id, t.peak, t.member_only, t.lighting, t.status, cb.reason
		FROM golf.tee_times t LEFT JOIN golf.course_blocks cb ON cb.id = t.block_id
		WHERE t.status <> 'closed' AND `+where+` ORDER BY t.start_at, t.start_tee`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Slot{}
	for rows.Next() {
		var s Slot
		var day time.Time
		var raw string
		if err := rows.Scan(&s.ID, &s.CourseID, &day, &s.StartAt, &s.StartTee, &s.Session, &s.DayTypeCode, &s.Capacity, &s.Used, &s.MinPlayers,
			&s.MaxPlayers, &s.Flights, &s.PlayingRouteID, &s.Peak, &s.MemberOnly, &s.Lighting, &raw, &s.BlockReason); err != nil {
			return nil, err
		}
		s.PlayDate = day.Format("2006-01-02")
		s.LocalTime = s.StartAt.In(loc).Format("15:04")
		s.Remaining = s.Capacity - s.Used
		if s.Remaining < 0 {
			s.Remaining = 0
		}
		s.Status = slotStatus(raw, s.Used, s.Capacity)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *Module) teeTimes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := prop(ctx)
	q := r.URL.Query()
	var out []Slot
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		loc := location(ctx, tx, p)
		day := localDay(clock.Now(), loc)
		if v := q.Get("date"); v != "" {
			d, err := parseDate(v)
			if err != nil {
				return err
			}
			day = d
		}
		where := "t.property_id = $1 AND t.play_date = $2::date"
		args := []any{p, day.Format("2006-01-02")}
		if v := q.Get("courseId"); v != "" {
			args = append(args, v)
			where += " AND t.course_id::text = $3"
		}
		var err error
		out, err = loadSlots(ctx, tx, loc, where, args...)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Slot]{Items: out})
}

// AvailableSlot is a slot with indicative prices per segment (FR-APP-02).
type AvailableSlot struct {
	Slot
	Prices map[string]string `json:"prices" doc:"Indicative price per segment (all-in, incl. tax when nett)"`
}

// availability lists bookable slots for a date with indicative prices.
func (m *Module) availability(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time, courseID *uuid.UUID, session string, players int,
	segments []string, channel string) ([]AvailableSlot, error) {
	loc := location(ctx, tx, property)
	where := "t.property_id = $1 AND t.play_date = $2::date"
	args := []any{property, day.Format("2006-01-02")}
	if courseID != nil {
		args = append(args, *courseID)
		where += fmt.Sprintf(" AND t.course_id = $%d", len(args))
	}
	if session != "" {
		args = append(args, session)
		where += fmt.Sprintf(" AND t.session = $%d", len(args))
	}
	slots, err := loadSlots(ctx, tx, loc, where, args...)
	if err != nil {
		return nil, err
	}
	now := clock.Now()
	out := []AvailableSlot{}
	cache := map[string]map[string]string{}
	for _, s := range slots {
		if s.StartAt.Before(now) {
			continue
		}
		if players > 0 && s.Remaining < players {
			s.Status = "full"
		}
		as := AvailableSlot{Slot: s, Prices: map[string]string{}}
		ck := s.Session + "|" + s.DayTypeCode + "|" + fmt.Sprint(s.Peak) + "|" + fmt.Sprint(s.PlayingRouteID)
		if p, ok := cache[ck]; ok {
			as.Prices = p
		} else {
			for _, seg := range segments {
				peak := s.Peak
				res, err := commercial.Resolve(ctx, tx, commercial.PriceQuery{Property: property, Segments: []string{seg}, PlayAt: s.StartAt, PlayDate: day,
					LocalTime: s.LocalTime, Session: s.Session, DayTypeCode: s.DayTypeCode, PlayingRouteID: s.PlayingRouteID, Channel: channel, Peak: &peak})
				if err == nil {
					as.Prices[seg] = res.Total
				}
			}
			cache[ck] = as.Prices
		}
		out = append(out, as)
	}
	return out, nil
}

func segmentsParam(r *http.Request, def ...string) []string {
	if v := r.URL.Query().Get("segments"); v != "" {
		return strings.Split(v, ",")
	}
	return def
}

func (m *Module) availabilityHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := prop(ctx)
	q := r.URL.Query()
	var out []AvailableSlot
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		day := localDay(clock.Now(), location(ctx, tx, p))
		if v := q.Get("date"); v != "" {
			d, err := parseDate(v)
			if err != nil {
				return err
			}
			day = d
		}
		var course *uuid.UUID
		if v := q.Get("courseId"); v != "" {
			u, err := uuid.Parse(v)
			if err != nil {
				return errs.BadRequest("invalid_course", "courseId must be a UUID")
			}
			course = &u
		}
		players := 0
		_, _ = fmt.Sscan(q.Get("players"), &players)
		var err error
		out, err = m.availability(ctx, tx, p, day, course, q.Get("session"), players, segmentsParam(r, "member", "guest_of_member", "non_member"), "back_office")
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[AvailableSlot]{Items: out})
}

// SheetPlayer is a player row on the tee sheet.
type SheetPlayer struct {
	ID                uuid.UUID  `json:"id"`
	BookingID         uuid.UUID  `json:"bookingId"`
	BookingCode       string     `json:"bookingCode"`
	Name              string     `json:"name"`
	PlayerType        string     `json:"playerType"`
	Segment           string     `json:"segment"`
	Status            string     `json:"status" enum:"booked,checked_in,no_show,cancelled,removed"`
	TBA               bool       `json:"tba"`
	CheckedInAt       *time.Time `json:"checkedInAt"`
	CaddyID           *uuid.UUID `json:"caddyId"`
	CaddyCode         *string    `json:"caddyCode"`
	CaddyName         *string    `json:"caddyName"`
	CaddyAssignmentID *uuid.UUID `json:"caddyAssignmentId" doc:"Rate the caddy after the round (Member App)"`
	BagTag            *string    `json:"bagTag"`
	Locker            *string    `json:"locker"`
}

// SheetFlight is a flight on the tee sheet.
type SheetFlight struct {
	ID            uuid.UUID     `json:"id"`
	FlightNo      int           `json:"flightNo"`
	BookingID     *uuid.UUID    `json:"bookingId"`
	BookingStatus *string       `json:"bookingStatus"`
	BookingType   *string       `json:"bookingType"`
	Status        string        `json:"status" enum:"confirmed,checked_in,ready,on_hold,in_play,completed,cancelled"`
	ReadyAt       *time.Time    `json:"readyAt"`
	TeeOffAt      *time.Time    `json:"teeOffAt"`
	FinishAt      *time.Time    `json:"roundFinishAt"`
	Players       []SheetPlayer `json:"players"`
	Carts         []string      `json:"golfCarts"`
	CartsNeeded   int           `json:"golfCartsNeeded"`
	CaddiesNeeded int           `json:"caddiesNeeded"`
	Readiness     Readiness     `json:"readiness"`
	QueueStatus   *string       `json:"queueStatus"`
}

// Readiness explains why a flight is (not) Ready (FR-CHK-06).
type Readiness struct {
	CheckedIn   int  `json:"checkedIn"`
	Players     int  `json:"players"`
	CaddiesOK   bool `json:"caddiesOk"`
	GolfCartsOK bool `json:"golfCartsOk"`
	PaymentOK   bool `json:"paymentOk"`
	Ready       bool `json:"ready"`
}

// SheetSlot is one row of the tee sheet grid.
type SheetSlot struct {
	Slot
	Flights []SheetFlight `json:"flights"`
}

// TeeSheet is the tee sheet of one course and day (FR-TEE-09).
type TeeSheet struct {
	CourseID  uuid.UUID    `json:"courseId"`
	Date      string       `json:"date"`
	Slots     []SheetSlot  `json:"slots"`
	Status    CourseStatus `json:"courseStatus"`
	Generated time.Time    `json:"generatedAt"`
}

// BuildTeeSheet loads the grid of a course for a day.
func (m *Module) BuildTeeSheet(ctx context.Context, q dbtx.Querier, property, courseID uuid.UUID, day time.Time) (TeeSheet, error) {
	loc := location(ctx, q, property)
	ts := TeeSheet{CourseID: courseID, Date: day.Format("2006-01-02"), Generated: clock.Now()}
	slots, err := loadSlots(ctx, q, loc, "t.property_id = $1 AND t.course_id = $2 AND t.play_date = $3::date", property, courseID, day.Format("2006-01-02"))
	if err != nil {
		return ts, err
	}
	pol, err := LoadPolicies(ctx, q, property, clock.Now())
	if err != nil {
		return ts, err
	}
	idx := map[uuid.UUID]int{}
	for i, s := range slots {
		ts.Slots = append(ts.Slots, SheetSlot{Slot: s, Flights: []SheetFlight{}})
		idx[s.ID] = i
	}
	if ts.Slots == nil {
		ts.Slots = []SheetSlot{}
	}
	flights, err := m.loadFlights(ctx, q, pol, "f.property_id = $1 AND f.course_id = $2 AND f.play_date = $3::date AND f.status <> 'cancelled'",
		property, courseID, day.Format("2006-01-02"))
	if err != nil {
		return ts, err
	}
	for _, f := range flights {
		if i, ok := idx[f.teeTimeID]; ok {
			ts.Slots[i].Flights = append(ts.Slots[i].Flights, f.SheetFlight)
		}
	}
	ts.Status, err = loadCourseStatus(ctx, q, courseID)
	return ts, err
}

type flightRow struct {
	SheetFlight
	teeTimeID uuid.UUID
}

func (m *Module) loadFlights(ctx context.Context, q dbtx.Querier, pol Policies, where string, args ...any) ([]flightRow, error) {
	rows, err := q.Query(ctx, `SELECT f.id, f.tee_time_id, f.flight_no, f.booking_id, b.status, b.booking_type, f.status, f.ready_at, f.tee_off_at,
		f.round_finish_at, sq.status, b.folio_id, b.payment_mode
		FROM golf.flights f LEFT JOIN golf.bookings b ON b.id = f.booking_id LEFT JOIN golf.starter_queue sq ON sq.flight_id = f.id
		WHERE `+where+` ORDER BY f.flight_no`, args...)
	if err != nil {
		return nil, err
	}
	type extra struct {
		folio *uuid.UUID
		mode  *string
	}
	var out []flightRow
	var ex []extra
	for rows.Next() {
		var f flightRow
		var e extra
		if err := rows.Scan(&f.ID, &f.teeTimeID, &f.FlightNo, &f.BookingID, &f.BookingStatus, &f.BookingType, &f.Status, &f.ReadyAt, &f.TeeOffAt,
			&f.FinishAt, &f.QueueStatus, &e.folio, &e.mode); err != nil {
			rows.Close()
			return nil, err
		}
		f.Players, f.Carts = []SheetPlayer{}, []string{}
		out = append(out, f)
		ex = append(ex, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(out))
	pos := map[uuid.UUID]int{}
	for i, f := range out {
		ids[i] = f.ID
		pos[f.ID] = i
	}
	pr, err := q.Query(ctx, `SELECT bp.id, bp.flight_id, bp.booking_id, b.code, bp.name, bp.player_type, bp.segment, bp.status, bp.tba, bp.checked_in_at,
		ca.caddy_id, c.code, c.name, ca.id, bd.tag_number, l.code
		FROM golf.booking_players bp JOIN golf.bookings b ON b.id = bp.booking_id
		LEFT JOIN LATERAL (SELECT id, caddy_id FROM golf.caddy_assignments x WHERE x.flight_id = bp.flight_id AND bp.id = ANY(x.player_ids)
			AND x.status IN ('assigned','in_play','completed') ORDER BY x.assigned_at DESC LIMIT 1) ca ON true
		LEFT JOIN golf.caddies c ON c.id = ca.caddy_id
		LEFT JOIN LATERAL (SELECT tag_number FROM golf.bag_drops d WHERE d.booking_player_id = bp.id ORDER BY d.dropped_at DESC LIMIT 1) bd ON true
		LEFT JOIN LATERAL (SELECT lk.code FROM golf.locker_assignments la JOIN golf.lockers lk ON lk.id = la.locker_id
			WHERE la.booking_player_id = bp.id AND la.status = 'active' LIMIT 1) l ON true
		WHERE bp.flight_id = ANY($1) AND bp.status NOT IN ('removed', 'cancelled') ORDER BY bp.seq`, ids)
	if err != nil {
		return nil, err
	}
	for pr.Next() {
		var sp SheetPlayer
		var fid uuid.UUID
		if err := pr.Scan(&sp.ID, &fid, &sp.BookingID, &sp.BookingCode, &sp.Name, &sp.PlayerType, &sp.Segment, &sp.Status, &sp.TBA, &sp.CheckedInAt,
			&sp.CaddyID, &sp.CaddyCode, &sp.CaddyName, &sp.CaddyAssignmentID, &sp.BagTag, &sp.Locker); err != nil {
			pr.Close()
			return nil, err
		}
		out[pos[fid]].Players = append(out[pos[fid]].Players, sp)
	}
	pr.Close()
	cr, err := q.Query(ctx, `SELECT a.flight_id, c.code FROM golf.golf_cart_assignments a JOIN golf.golf_carts c ON c.id = a.golf_cart_id
		WHERE a.flight_id = ANY($1) AND a.status IN ('assigned', 'in_use', 'returned') ORDER BY c.code`, ids)
	if err != nil {
		return nil, err
	}
	for cr.Next() {
		var fid uuid.UUID
		var code string
		if err := cr.Scan(&fid, &code); err != nil {
			cr.Close()
			return nil, err
		}
		out[pos[fid]].Carts = append(out[pos[fid]].Carts, code)
	}
	cr.Close()
	for i := range out {
		f := &out[i]
		n := len(f.Players)
		f.CartsNeeded = ceilDiv(n, pol.Cart.PlayersPerCart)
		f.CaddiesNeeded = ceilDiv(n, pol.Caddy.PlayersPerCaddy)
		rd := Readiness{Players: n, PaymentOK: true}
		caddied := 0
		for _, p := range f.Players {
			if p.Status == "checked_in" {
				rd.CheckedIn++
			}
			if p.CaddyID != nil {
				caddied++
			}
		}
		rd.CaddiesOK = !pol.Caddy.Mandatory || caddied >= n
		rd.GolfCartsOK = !pol.Cart.Mandatory || len(f.Carts) >= f.CartsNeeded
		if pol.Payment.PayBeforeCheckIn && ex[i].folio != nil && ex[i].mode != nil && (*ex[i].mode == "pay_at_venue" || *ex[i].mode == "deposit") {
			if sum, err := billing.FolioSummary(ctx, q, *ex[i].folio); err == nil {
				rd.PaymentOK = !decPositive(sum.Balance) || decPositive(sum.HeldDeposits)
			}
		}
		rd.Ready = n > 0 && rd.CheckedIn == n && rd.CaddiesOK && rd.GolfCartsOK
		f.Readiness = rd
	}
	return out, nil
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return a
	}
	return (a + b - 1) / b
}

func (m *Module) teeSheetHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := prop(ctx)
	q := r.URL.Query()
	cid, err := uuid.Parse(q.Get("courseId"))
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("course_required", "courseId is required", errs.Field("courseId", "required", "choose a course")))
		return
	}
	var out TeeSheet
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		day := localDay(clock.Now(), location(ctx, tx, p))
		if v := q.Get("date"); v != "" {
			d, err := parseDate(v)
			if err != nil {
				return err
			}
			day = d
		}
		out, err = m.BuildTeeSheet(ctx, tx, p, cid, day)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ── course blocks (FR-CRS-05) ─────────────────────────────────────────────

type BlockRequest struct {
	CourseID       uuid.UUID  `json:"courseId"`
	PlayingRouteID *uuid.UUID `json:"playingRouteId,omitempty"`
	StartsAt       time.Time  `json:"startsAt"`
	EndsAt         time.Time  `json:"endsAt"`
	Reason         string     `json:"reason" enum:"maintenance,tournament,private_event,weather_closure,management_hold"`
	Notes          string     `json:"notes,omitempty"`
}

type Block struct {
	ID               uuid.UUID         `json:"id"`
	CourseID         uuid.UUID         `json:"courseId"`
	PlayingRouteID   *uuid.UUID        `json:"playingRouteId"`
	StartsAt         time.Time         `json:"startsAt"`
	EndsAt           time.Time         `json:"endsAt"`
	Reason           string            `json:"reason"`
	Notes            *string           `json:"notes"`
	Status           string            `json:"status" enum:"active,cancelled"`
	BlockedSlots     int               `json:"blockedSlots"`
	AffectedBookings []AffectedBooking `json:"affectedBookings"`
	CreatedAt        time.Time         `json:"createdAt"`
}

type AffectedBooking struct {
	BookingID   uuid.UUID `json:"bookingId"`
	Code        string    `json:"code"`
	ContactName string    `json:"contactName"`
	StartAt     time.Time `json:"startAt"`
	Status      string    `json:"status"`
	Players     int       `json:"players"`
}

func loadBlock(ctx context.Context, q dbtx.Querier, bid uuid.UUID) (Block, error) {
	var b Block
	err := q.QueryRow(ctx, `SELECT id, course_id, playing_route_id, starts_at, ends_at, reason, notes, status, created_at,
		(SELECT count(*) FROM golf.tee_times WHERE block_id = cb.id) FROM golf.course_blocks cb WHERE id = $1`, bid).
		Scan(&b.ID, &b.CourseID, &b.PlayingRouteID, &b.StartsAt, &b.EndsAt, &b.Reason, &b.Notes, &b.Status, &b.CreatedAt, &b.BlockedSlots)
	if dbtx.IsNoRows(err) {
		return b, errs.NotFound("course block")
	}
	if err != nil {
		return b, err
	}
	b.AffectedBookings = []AffectedBooking{}
	rows, err := q.Query(ctx, `SELECT DISTINCT b.id, b.code, b.contact_name, t.start_at, b.status, b.player_count
		FROM golf.bookings b JOIN golf.flights f ON f.booking_id = b.id JOIN golf.tee_times t ON t.id = f.tee_time_id
		WHERE t.course_id = $1 AND t.start_at >= $2 AND t.start_at < $3 AND b.status IN ('draft', 'pending', 'confirmed', 'checked_in')
		  AND ($4::uuid IS NULL OR t.playing_route_id IS NULL OR t.playing_route_id = $4) ORDER BY t.start_at`, b.CourseID, b.StartsAt, b.EndsAt, b.PlayingRouteID)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var a AffectedBooking
		if err := rows.Scan(&a.BookingID, &a.Code, &a.ContactName, &a.StartAt, &a.Status, &a.Players); err != nil {
			return b, err
		}
		b.AffectedBookings = append(b.AffectedBookings, a)
	}
	return b, rows.Err()
}

func (m *Module) createBlock(w http.ResponseWriter, r *http.Request) {
	var req BlockRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	switch req.Reason {
	case "maintenance", "tournament", "private_event", "weather_closure", "management_hold":
	default:
		httpx.WriteError(w, r, errs.Validation("invalid_reason", "invalid reason", errs.Field("reason", "invalid",
			"maintenance, tournament, private_event, weather_closure or management_hold")))
		return
	}
	if !req.EndsAt.After(req.StartsAt) {
		httpx.WriteError(w, r, errs.Validation("invalid_period", "end must be after start", errs.Field("endsAt", "invalid", "after start")))
		return
	}
	ctx := r.Context()
	p := prop(ctx)
	var out Block
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var courseName string
		if err := tx.QueryRow(ctx, `SELECT name FROM golf.courses WHERE id = $1 AND property_id = $2`, req.CourseID, p).Scan(&courseName); err != nil {
			return errs.Validation("invalid_course", "course not found", errs.Field("courseId", "not_found", "course not found"))
		}
		bid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO golf.course_blocks (id, property_id, course_id, playing_route_id, starts_at, ends_at, reason, notes, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, bid, p, req.CourseID, req.PlayingRouteID, req.StartsAt, req.EndsAt, req.Reason, nullStr(req.Notes),
			id.Ptr(actor(ctx))); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Validation("invalid_route", "playing route not found", errs.Field("playingRouteId", "not_found", "route not found"))
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.tee_times SET status = 'blocked', block_id = $1 WHERE course_id = $2 AND start_at >= $3 AND start_at < $4
			AND status = 'open' AND ($5::uuid IS NULL OR playing_route_id IS NULL OR playing_route_id = $5)`,
			bid, req.CourseID, req.StartsAt, req.EndsAt, req.PlayingRouteID); err != nil {
			return err
		}
		var err error
		if out, err = loadBlock(ctx, tx, bid); err != nil {
			return err
		}
		loc := location(ctx, tx, p)
		if len(out.AffectedBookings) > 0 {
			users, err := notification.UserIDsWithPermission(ctx, tx, "golf.booking.update", &p)
			if err != nil {
				return err
			}
			if err := m.Notify.Send(ctx, tx, notify.Message{Event: "golf.bookings_affected", Category: "general", UserIDs: users, PropertyID: &p,
				Link: m.Cfg.PublicBaseURL + "/golf/bookings", Data: map[string]any{"count": len(out.AffectedBookings), "reason": strings.ReplaceAll(req.Reason, "_", " "),
					"course": courseName, "period": req.StartsAt.In(loc).Format("02 Jan 15:04") + " – " + req.EndsAt.In(loc).Format("15:04"),
					"link": m.Cfg.PublicBaseURL + "/golf/bookings"}}); err != nil {
				return err
			}
		}
		if err := notifyRealtime(ctx, tx, p, "golf.tee_sheet", "blocked", req.CourseID, localDay(req.StartsAt, loc), nil); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionCreate, EntityType: "golf.course_block", EntityID: bid.String(),
			EntityLabel: courseName + " " + req.Reason, PropertyID: &p, After: map[string]any{"startsAt": req.StartsAt, "endsAt": req.EndsAt, "reason": req.Reason,
				"blockedSlots": out.BlockedSlots, "affectedBookings": len(out.AffectedBookings)}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (m *Module) listBlocks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := prop(ctx)
	out := []Block{}
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		args := []any{p}
		where := "property_id = $1"
		if v := r.URL.Query().Get("courseId"); v != "" {
			args = append(args, v)
			where += " AND course_id::text = $2"
		}
		if r.URL.Query().Get("all") != "true" {
			where += " AND (status = 'active' AND ends_at > now())"
		}
		rows, err := tx.Query(ctx, `SELECT id FROM golf.course_blocks WHERE `+where+` ORDER BY starts_at LIMIT 200`, args...)
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var x uuid.UUID
			if err := rows.Scan(&x); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, x)
		}
		rows.Close()
		for _, x := range ids {
			b, err := loadBlock(ctx, tx, x)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Block]{Items: out})
}

func (m *Module) cancelBlock(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	p := prop(ctx)
	var out Block
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var course uuid.UUID
		var starts time.Time
		err := tx.QueryRow(ctx, `UPDATE golf.course_blocks SET status = 'cancelled', updated_by = $3 WHERE id = $1 AND property_id = $2 AND status = 'active'
			RETURNING course_id, starts_at`, bid, p, id.Ptr(actor(ctx))).Scan(&course, &starts)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("active course block")
		}
		if err != nil {
			return err
		}
		// re-open the slots unless another active block still covers them
		if _, err := tx.Exec(ctx, `UPDATE golf.tee_times t SET block_id = (SELECT cb.id FROM golf.course_blocks cb WHERE cb.course_id = t.course_id
			AND cb.status = 'active' AND cb.starts_at <= t.start_at AND cb.ends_at > t.start_at LIMIT 1) WHERE t.block_id = $1`, bid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.tee_times SET status = CASE WHEN block_id IS NULL THEN 'open' ELSE 'blocked' END
			WHERE course_id = $1 AND status = 'blocked' AND (block_id IS NULL OR block_id <> $2)`, course, bid); err != nil {
			return err
		}
		if out, err = loadBlock(ctx, tx, bid); err != nil {
			return err
		}
		if err := notifyRealtime(ctx, tx, p, "golf.tee_sheet", "unblocked", course, localDay(starts, location(ctx, tx, p)), nil); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionStatusChange, EntityType: "golf.course_block", EntityID: bid.String(),
			PropertyID: &p, After: map[string]any{"status": "cancelled"}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ── playing route holes (FR-CRS-03) ───────────────────────────────────────

type RouteHole struct {
	Sequence    int             `json:"sequence"`
	HoleID      uuid.UUID       `json:"holeId"`
	Number      int             `json:"number"`
	Par         int             `json:"par"`
	StrokeIndex *int            `json:"strokeIndex"`
	SectionCode string          `json:"sectionCode"`
	Distances   json.RawMessage `json:"distances"`
}

type RouteHoles struct {
	RouteID   uuid.UUID   `json:"routeId"`
	Code      string      `json:"code"`
	Name      string      `json:"name"`
	HoleCount int         `json:"holeCount"`
	Par       int         `json:"par"`
	Holes     []RouteHole `json:"holes"`
}

// LoadRouteHoles derives the ordered holes of a playing route.
func LoadRouteHoles(ctx context.Context, q dbtx.Querier, routeID uuid.UUID) (RouteHoles, error) {
	var rh RouteHoles
	var codes string
	var course uuid.UUID
	err := q.QueryRow(ctx, `SELECT id, code, name, section_codes, course_id FROM golf.playing_routes WHERE id = $1`, routeID).Scan(&rh.RouteID, &rh.Code, &rh.Name, &codes, &course)
	if dbtx.IsNoRows(err) {
		return rh, errs.NotFound("playing route")
	}
	if err != nil {
		return rh, err
	}
	rh.Holes = []RouteHole{}
	seq := 0
	for _, sc := range strings.Split(codes, ",") {
		rows, err := q.Query(ctx, `SELECT h.id, h.number, h.par, h.stroke_index, s.code, h.distances FROM golf.holes h JOIN golf.course_sections s ON s.id = h.section_id
			WHERE s.course_id = $1 AND s.code = $2 AND h.status = 'active' ORDER BY h.number`, course, sc)
		if err != nil {
			return rh, err
		}
		for rows.Next() {
			var h RouteHole
			if err := rows.Scan(&h.HoleID, &h.Number, &h.Par, &h.StrokeIndex, &h.SectionCode, &h.Distances); err != nil {
				rows.Close()
				return rh, err
			}
			seq++
			h.Sequence = seq
			rh.Par += h.Par
			rh.Holes = append(rh.Holes, h)
		}
		rows.Close()
	}
	rh.HoleCount = len(rh.Holes)
	return rh, nil
}

func (m *Module) routeHolesHTTP(w http.ResponseWriter, r *http.Request) {
	rid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out RouteHoles
	err = m.DB.WithReadTx(r.Context(), func(tx pgx.Tx) error {
		out, err = LoadRouteHoles(r.Context(), tx, rid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func sortSlots(s []Slot) {
	sort.Slice(s, func(i, j int) bool { return s[i].StartAt.Before(s[j].StartAt) })
}

var _ = sortSlots
