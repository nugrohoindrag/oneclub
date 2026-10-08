package golf

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/resource"
	syncsvc "oneclub/internal/platform/sync"
)

// tx runs fn in a write transaction and writes the JSON result.
func (m *Module) write(w http.ResponseWriter, r *http.Request, status int, fn func(ctx context.Context, tx pgx.Tx) (any, error)) {
	ctx := r.Context()
	var out any
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = fn(ctx, tx)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, status, out)
}

func (m *Module) read(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, tx pgx.Tx) (any, error)) {
	ctx := r.Context()
	var out any
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = fn(ctx, tx)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func decode[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	if err := httpx.Decode(r, &v); err != nil {
		httpx.WriteError(w, r, err)
		return v, false
	}
	return v, true
}

func dayParam(ctx context.Context, tx pgx.Tx, r *http.Request) (time.Time, error) {
	if v := r.URL.Query().Get("date"); v != "" {
		return parseDate(v)
	}
	return localDay(clock.Now(), location(ctx, tx, prop(ctx))), nil
}

// ── bookings ──────────────────────────────────────────────────────────────

// BookingSummary is the list view.
type BookingSummary struct {
	ID          uuid.UUID `json:"id"`
	Code        string    `json:"code"`
	BookingType string    `json:"bookingType"`
	Channel     string    `json:"channel"`
	Status      string    `json:"status"`
	CourseID    uuid.UUID `json:"courseId"`
	CourseName  string    `json:"courseName"`
	PlayDate    string    `json:"playDate"`
	StartAt     time.Time `json:"startAt"`
	LocalTime   string    `json:"localTime"`
	PlayerCount int       `json:"playerCount"`
	ContactName string    `json:"contactName"`
	PaymentMode *string   `json:"paymentMode"`
	Total       *string   `json:"total"`
	CreatedAt   time.Time `json:"createdAt"`
}

func listBookings(ctx context.Context, q dbtx.Querier, property uuid.UUID, r *http.Request, extraWhere string, extraArgs ...any) (httpx.Page[BookingSummary], error) {
	lp := httpx.ParseList(r)
	loc := location(ctx, q, property)
	where := []string{"b.property_id = $1", "b.status <> 'draft'"}
	args := []any{property}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if extraWhere != "" {
		args = append(args, extraArgs...)
		where = append(where, extraWhere)
	}
	if v := lp.Filters["status"]; v != "" {
		add("b.status = ANY(?)", strings.Split(v, ","))
	}
	if v := lp.Filters["bookingType"]; v != "" {
		add("b.booking_type = ?", v)
	}
	if v := lp.Filters["channel"]; v != "" {
		add("b.channel = ?", v)
	}
	if v := lp.Filters["courseId"]; v != "" {
		add("b.course_id::text = ?", v)
	}
	if v := lp.Filters["customerId"]; v != "" {
		add("(b.customer_id::text = ? OR EXISTS (SELECT 1 FROM golf.booking_players x WHERE x.booking_id = b.id AND x.customer_id::text = ?))", v)
	}
	q2 := r.URL.Query()
	if v := q2.Get("date"); v != "" {
		add("b.play_date = ?::date", v)
	}
	if v := q2.Get("from"); v != "" {
		add("b.play_date >= ?::date", v)
	}
	if v := q2.Get("to"); v != "" {
		add("b.play_date <= ?::date", v)
	}
	if lp.Q != "" {
		add("(b.code ILIKE ? OR b.contact_name ILIKE ? OR EXISTS (SELECT 1 FROM golf.booking_players x WHERE x.booking_id = b.id AND x.name ILIKE ?))", "%"+lp.Q+"%")
	}
	offset := 0
	if c, err := httpx.DecodeCursor(lp.Cursor); err == nil && c != "" {
		offset, _ = strconv.Atoi(c)
	}
	order := "b.start_at, b.code"
	if q2.Get("sort") == "-createdAt" {
		order = "b.created_at DESC"
	}
	rows, err := q.Query(ctx, `SELECT b.id, b.code, b.booking_type, b.channel, b.status, b.course_id, c.name, b.play_date, b.start_at, b.player_count, b.contact_name,
		b.payment_mode, b.folio_id, b.created_at FROM golf.bookings b JOIN golf.courses c ON c.id = b.course_id WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(" ORDER BY %s LIMIT %d OFFSET %d", order, lp.PageSize+1, offset), args...)
	if err != nil {
		return httpx.Page[BookingSummary]{}, err
	}
	out := []BookingSummary{}
	var folios []*uuid.UUID
	for rows.Next() {
		var b BookingSummary
		var d time.Time
		var folio *uuid.UUID
		if err := rows.Scan(&b.ID, &b.Code, &b.BookingType, &b.Channel, &b.Status, &b.CourseID, &b.CourseName, &d, &b.StartAt, &b.PlayerCount, &b.ContactName,
			&b.PaymentMode, &folio, &b.CreatedAt); err != nil {
			rows.Close()
			return httpx.Page[BookingSummary]{}, err
		}
		b.PlayDate, b.LocalTime = d.Format("2006-01-02"), b.StartAt.In(loc).Format("15:04")
		out = append(out, b)
		folios = append(folios, folio)
	}
	rows.Close()
	for i, f := range folios {
		if f == nil {
			continue
		}
		if s, err := billingSummary(ctx, q, *f); err == nil {
			out[i].Total = &s.Charges
		}
	}
	p := httpx.Page[BookingSummary]{Items: out}
	if len(out) > lp.PageSize {
		p.Items = out[:lp.PageSize]
		p.NextCursor = httpx.EncodeCursor(strconv.Itoa(offset + lp.PageSize))
	}
	return p, nil
}

func (m *Module) bookingsHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) { return listBookings(ctx, tx, prop(ctx), r, "") })
}

func (m *Module) bookingHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var p uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT property_id FROM golf.bookings WHERE id = $1`, bid).Scan(&p); err != nil || p != prop(ctx) {
			return nil, errs.NotFound("booking")
		}
		return GetBooking(ctx, tx, bid)
	})
}

func (m *Module) holdHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[HoldRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		h, err := m.PlaceHold(ctx, tx, prop(ctx), req, nil, true)
		h.HoldToken = ""
		return h, err
	})
}

func (m *Module) releaseHoldHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		p := prop(ctx)
		if err := m.ReleaseHold(ctx, tx, p, bid, "released by staff"); err != nil {
			return nil, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "release_hold", EntityType: "golf.booking", EntityID: bid.String(), PropertyID: &p}); err != nil {
			return nil, err
		}
		return GetBooking(ctx, tx, bid)
	})
}

func (m *Module) createBookingHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[BookingRequest](w, r)
	if !ok {
		return
	}
	if req.Channel == "" || req.Channel == "member_app" || req.Channel == "website" {
		req.Channel = "back_office"
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return m.CreateBooking(ctx, tx, prop(ctx), req, true)
	})
}

func (m *Module) confirmHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		p := prop(ctx)
		b, err := lockBooking(ctx, tx, p, bid)
		if err != nil {
			return nil, err
		}
		if b.Status != "pending" {
			return nil, errs.Conflict("not_pending", "only pending bookings can be confirmed")
		}
		// staff confirm a pending booking (e.g. deposit taken by transfer); the
		// balance is then paid at the venue
		if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET payment_mode = 'pay_at_venue' WHERE id = $1`, bid); err != nil {
			return nil, err
		}
		if err := m.confirm(ctx, tx, p, bid, "confirmed by staff"); err != nil {
			return nil, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "confirm", EntityType: "golf.booking", EntityID: bid.String(), EntityLabel: b.Code, PropertyID: &p}); err != nil {
			return nil, err
		}
		return GetBooking(ctx, tx, bid)
	})
}

func (m *Module) cancelHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[CancelRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.Cancel(ctx, tx, prop(ctx), bid, req) })
}

func (m *Module) rescheduleHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[RescheduleRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return m.Reschedule(ctx, tx, prop(ctx), bid, req, true)
	})
}

type ReasonBody struct {
	Reason string `json:"reason,omitempty"`
}

func (m *Module) noShowHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[ReasonBody](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return m.NoShow(ctx, tx, prop(ctx), bid, req.Reason)
	})
}

type AddPlayerRequest struct {
	Player   PlayerInput `json:"player"`
	FlightID *uuid.UUID  `json:"flightId,omitempty"`
}

func (m *Module) addPlayerHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[AddPlayerRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		b, err := m.AddPlayer(ctx, tx, prop(ctx), bid, req.Player, req.FlightID)
		if err != nil {
			return nil, err
		}
		p := prop(ctx)
		return b, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "add_player", EntityType: "golf.booking", EntityID: bid.String(), EntityLabel: b.Code,
			PropertyID: &p, After: req.Player})
	})
}

func (m *Module) patchPlayerHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pid, err := httpx.PathUUID(r, "playerId")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[PlayerPatch](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		b, err := m.UpdatePlayer(ctx, tx, prop(ctx), bid, pid, req)
		if err != nil {
			return nil, err
		}
		p := prop(ctx)
		return b, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "update_player", EntityType: "golf.booking", EntityID: bid.String(), EntityLabel: b.Code,
			PropertyID: &p, After: req})
	})
}

func (m *Module) removePlayerHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pid, err := httpx.PathUUID(r, "playerId")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		b, err := m.RemovePlayer(ctx, tx, prop(ctx), bid, pid, r.URL.Query().Get("reason"))
		if err != nil {
			return nil, err
		}
		p := prop(ctx)
		return b, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "remove_player", EntityType: "golf.booking", EntityID: bid.String(), EntityLabel: b.Code,
			PropertyID: &p, Reason: r.URL.Query().Get("reason"), Before: map[string]any{"playerId": pid}})
	})
}

// HistoryEntry is one booking modification.
type HistoryEntry struct {
	ID         uuid.UUID       `json:"id"`
	Event      string          `json:"event"`
	FromValue  json.RawMessage `json:"from"`
	ToValue    json.RawMessage `json:"to"`
	Reason     *string         `json:"reason"`
	ActorName  *string         `json:"actorName"`
	OccurredAt time.Time       `json:"occurredAt"`
}

func (m *Module) historyHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT id, event, coalesce(from_value, 'null'::jsonb), coalesce(to_value, 'null'::jsonb), reason, actor_name, occurred_at
			FROM golf.booking_history WHERE booking_id = $1 AND property_id = $2 ORDER BY occurred_at, id`, bid, prop(ctx))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []HistoryEntry{}
		for rows.Next() {
			var h HistoryEntry
			if err := rows.Scan(&h.ID, &h.Event, &h.FromValue, &h.ToValue, &h.Reason, &h.ActorName, &h.OccurredAt); err != nil {
				return nil, err
			}
			out = append(out, h)
		}
		return httpx.Page[HistoryEntry]{Items: out}, rows.Err()
	})
}

// ── flights ───────────────────────────────────────────────────────────────

type CreateFlightRequest struct {
	TeeTimeID uuid.UUID  `json:"teeTimeId"`
	BookingID *uuid.UUID `json:"bookingId,omitempty"`
}

type FlightRef struct {
	ID uuid.UUID `json:"id"`
}

func (m *Module) createFlightHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[CreateFlightRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		p := prop(ctx)
		fid, err := m.CreateFlight(ctx, tx, p, req.TeeTimeID, req.BookingID)
		if err != nil {
			return nil, err
		}
		return FlightRef{ID: fid}, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionCreate, EntityType: "golf.flight", EntityID: fid.String(), PropertyID: &p})
	})
}

type MovePlayerRequest struct {
	PlayerID uuid.UUID `json:"playerId"`
}

func (m *Module) movePlayerHTTP(w http.ResponseWriter, r *http.Request) {
	fid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[MovePlayerRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		p := prop(ctx)
		b, err := m.MovePlayer(ctx, tx, p, fid, req.PlayerID)
		if err != nil {
			return nil, err
		}
		return b, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "assign_player", EntityType: "golf.flight", EntityID: fid.String(), PropertyID: &p,
			After: map[string]any{"playerId": req.PlayerID}})
	})
}

func (m *Module) flightsHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		pol, err := LoadPolicies(ctx, tx, prop(ctx), clock.Now())
		if err != nil {
			return nil, err
		}
		where := "f.property_id = $1 AND f.play_date = $2::date AND f.status <> 'cancelled'"
		args := []any{prop(ctx), day.Format("2006-01-02")}
		if v := r.URL.Query().Get("filter[status]"); v != "" {
			args = append(args, strings.Split(v, ","))
			where += " AND f.status = ANY($3)"
		}
		fl, err := m.loadFlights(ctx, tx, pol, where, args...)
		if err != nil {
			return nil, err
		}
		out := make([]SheetFlight, 0, len(fl))
		for _, f := range fl {
			out = append(out, f.SheetFlight)
		}
		return httpx.Page[SheetFlight]{Items: out}, nil
	})
}

// PlayerRow is the Players list (Golf → Players).
type PlayerRow struct {
	ID          uuid.UUID  `json:"id"`
	BookingID   uuid.UUID  `json:"bookingId"`
	BookingCode string     `json:"bookingCode"`
	Name        string     `json:"name"`
	PlayerType  string     `json:"playerType"`
	Segment     string     `json:"segment"`
	Status      string     `json:"status"`
	LocalTime   string     `json:"localTime"`
	CustomerID  *uuid.UUID `json:"customerId"`
	PriceTotal  *string    `json:"priceTotal"`
}

func (m *Module) playersHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		loc := location(ctx, tx, prop(ctx))
		rows, err := tx.Query(ctx, `SELECT bp.id, bp.booking_id, b.code, bp.name, bp.player_type, bp.segment, bp.status, t.start_at, bp.customer_id, bp.price_total::text
			FROM golf.booking_players bp JOIN golf.bookings b ON b.id = bp.booking_id JOIN golf.flights f ON f.id = bp.flight_id JOIN golf.tee_times t ON t.id = f.tee_time_id
			WHERE bp.property_id = $1 AND f.play_date = $2::date AND bp.status NOT IN ('removed') ORDER BY t.start_at, bp.seq`, prop(ctx), day.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []PlayerRow{}
		for rows.Next() {
			var p PlayerRow
			var at time.Time
			if err := rows.Scan(&p.ID, &p.BookingID, &p.BookingCode, &p.Name, &p.PlayerType, &p.Segment, &p.Status, &at, &p.CustomerID, &p.PriceTotal); err != nil {
				return nil, err
			}
			p.LocalTime = at.In(loc).Format("15:04")
			out = append(out, p)
		}
		return httpx.Page[PlayerRow]{Items: out}, rows.Err()
	})
}

// ── rain checks ───────────────────────────────────────────────────────────

type RainCheckRequest struct {
	FlightID    uuid.UUID `json:"flightId"`
	HolesPlayed int       `json:"holesPlayed"`
}

type RainCheckBatchRequest struct {
	Flights []RainCheckRequest `json:"flights"`
}

func (m *Module) issueRainCheckHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[RainCheckRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out, err := m.IssueRainChecks(ctx, tx, prop(ctx), map[uuid.UUID]int{req.FlightID: req.HolesPlayed})
		return httpx.Page[RainCheck]{Items: out}, err
	})
}

func (m *Module) batchRainCheckHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[RainCheckBatchRequest](w, r)
	if !ok {
		return
	}
	if len(req.Flights) == 0 {
		httpx.WriteError(w, r, errs.Validation("flights_required", "choose the stopped flights", errs.Field("flights", "required", "one or more")))
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		fl := map[uuid.UUID]int{}
		for _, f := range req.Flights {
			fl[f.FlightID] = f.HolesPlayed
		}
		out, err := m.IssueRainChecks(ctx, tx, prop(ctx), fl)
		return httpx.Page[RainCheck]{Items: out}, err
	})
}

func (m *Module) rainChecksHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		where := "r.property_id = $1"
		args := []any{prop(ctx)}
		if v := r.URL.Query().Get("filter[customerId]"); v != "" {
			args = append(args, v)
			where += " AND r.customer_id::text = $2"
		}
		rows, err := tx.Query(ctx, `SELECT `+rainCheckCols+` WHERE `+where+` ORDER BY r.created_at DESC LIMIT 500`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []RainCheck{}
		for rows.Next() {
			rc, err := scanRainCheck(rows)
			if err != nil {
				return nil, err
			}
			if s := r.URL.Query().Get("filter[status]"); s != "" && s != rc.Status {
				continue
			}
			out = append(out, rc)
		}
		return httpx.Page[RainCheck]{Items: out}, rows.Err()
	})
}

// ── check-in, starter, course status ──────────────────────────────────────

func (m *Module) lookupHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out, err := m.findForCheckIn(ctx, tx, prop(ctx), CheckInRequest{Method: q.Get("method"), Value: q.Get("value"), Date: q.Get("date")})
		return httpx.Page[CheckInCandidate]{Items: out}, err
	})
}

func (m *Module) checkInHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[CheckInRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		method := req.Method
		if method == "" {
			method = "booking_code"
		}
		return m.CheckIn(ctx, tx, prop(ctx), req, method)
	})
}

func (m *Module) queueHTTP(w http.ResponseWriter, r *http.Request) {
	cid, err := uuid.Parse(r.URL.Query().Get("courseId"))
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("course_required", "courseId is required", errs.Field("courseId", "required", "course")))
		return
	}
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		return m.LoadQueue(ctx, tx, prop(ctx), cid, day)
	})
}

func (m *Module) controlHTTP(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fid, err := httpx.PathUUID(r, "flightId")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		req, ok := decode[StarterAction](w, r)
		if !ok {
			return
		}
		m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
			return m.Control(ctx, tx, prop(ctx), fid, action, req)
		})
	}
}

func (m *Module) courseStatusListHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT id FROM golf.courses WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, prop(ctx))
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
		out := []CourseStatus{}
		for _, x := range ids {
			cs, err := loadCourseStatus(ctx, tx, x)
			if err != nil {
				return nil, err
			}
			out = append(out, cs)
		}
		return httpx.Page[CourseStatus]{Items: out}, nil
	})
}

func (m *Module) courseStatusHTTP(w http.ResponseWriter, r *http.Request) {
	cid, err := httpx.PathUUID(r, "courseId")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[CourseStatusRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return m.UpdateCourseStatus(ctx, tx, prop(ctx), cid, req)
	})
}

// ── caddy & cart & locker & bag ───────────────────────────────────────────

func (m *Module) caddyBoardHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		out, err := CaddyBoard(ctx, tx, prop(ctx), day, location(ctx, tx, prop(ctx)))
		return httpx.Page[CaddyBoardEntry]{Items: out}, err
	})
}

func (m *Module) attendanceHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[AttendanceRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out, err := m.RecordAttendance(ctx, tx, prop(ctx), req)
		return httpx.Page[CaddyBoardEntry]{Items: out}, err
	})
}

func (m *Module) reorderHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[ReorderRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out, err := m.ReorderQueue(ctx, tx, prop(ctx), req)
		return httpx.Page[CaddyBoardEntry]{Items: out}, err
	})
}

func (m *Module) assignCaddyHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[CaddyAssignRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out, err := m.AssignCaddies(ctx, tx, prop(ctx), req)
		return httpx.Page[CaddyAssignment]{Items: out}, err
	})
}

func (m *Module) replaceCaddyHTTP(w http.ResponseWriter, r *http.Request) {
	aid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[ReplaceRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out, err := m.ReplaceCaddy(ctx, tx, prop(ctx), aid, req)
		return httpx.Page[CaddyAssignment]{Items: out}, err
	})
}

func (m *Module) cancelCaddyHTTP(w http.ResponseWriter, r *http.Request) {
	aid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[ReasonBody](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		p := prop(ctx)
		var flight uuid.UUID
		var day time.Time
		err := tx.QueryRow(ctx, `UPDATE golf.caddy_assignments SET status = 'cancelled', replace_reason = $3 WHERE id = $1 AND property_id = $2 AND status = 'assigned'
			RETURNING flight_id, play_date`, aid, p, nullStr(req.Reason)).Scan(&flight, &day)
		if dbtx.IsNoRows(err) {
			return nil, errs.NotFound("assigned caddy")
		}
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'checked_in', ready_at = NULL WHERE id = $1 AND status = 'ready'`, flight); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.starter_queue SET status = 'removed' WHERE flight_id = $1 AND status = 'waiting'`, flight); err != nil {
			return nil, err
		}
		if err := realtimeBoards(ctx, tx, p, day); err != nil {
			return nil, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "cancel_caddy_assignment", EntityType: "golf.caddy_assignment", EntityID: aid.String(),
			PropertyID: &p, Reason: req.Reason}); err != nil {
			return nil, err
		}
		out, err := ListCaddyAssignments(ctx, tx, location(ctx, tx, p), "a.id = $1", aid)
		return httpx.Page[CaddyAssignment]{Items: out}, err
	})
}

func (m *Module) caddyAssignmentsHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		q := r.URL.Query()
		where := "a.property_id = $1"
		args := []any{prop(ctx)}
		if v := q.Get("caddyId"); v != "" {
			args = append(args, v)
			where += fmt.Sprintf(" AND a.caddy_id::text = $%d", len(args))
		}
		if v := q.Get("date"); v != "" {
			args = append(args, v)
			where += fmt.Sprintf(" AND a.play_date = $%d::date", len(args))
		}
		if v := q.Get("flightId"); v != "" {
			args = append(args, v)
			where += fmt.Sprintf(" AND a.flight_id::text = $%d", len(args))
		}
		out, err := ListCaddyAssignments(ctx, tx, location(ctx, tx, prop(ctx)), where, args...)
		return httpx.Page[CaddyAssignment]{Items: out}, err
	})
}

func (m *Module) tipHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[TipRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.AddTip(ctx, tx, prop(ctx), req) })
}

func (m *Module) tipsHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT t.id, t.caddy_id, c.name, t.assignment_id, t.amount::text, t.method, t.tip_date, t.folio_line_id, t.created_at
			FROM golf.caddy_tips t JOIN golf.caddies c ON c.id = t.caddy_id WHERE t.property_id = $1 AND t.tip_date = $2::date ORDER BY c.code, t.created_at`,
			prop(ctx), day.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []CaddyTip{}
		for rows.Next() {
			var t CaddyTip
			var d time.Time
			if err := rows.Scan(&t.ID, &t.CaddyID, &t.CaddyName, &t.AssignmentID, &t.Amount, &t.Method, &d, &t.FolioLineID, &t.CreatedAt); err != nil {
				return nil, err
			}
			t.TipDate = d.Format("2006-01-02")
			out = append(out, t)
		}
		return httpx.Page[CaddyTip]{Items: out}, rows.Err()
	})
}

func (m *Module) cartBoardHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		out, err := CartBoard(ctx, tx, prop(ctx), day)
		return httpx.Page[CartBoardEntry]{Items: out}, err
	})
}

func (m *Module) readinessHTTP(w http.ResponseWriter, r *http.Request) {
	cid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[ReadinessRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return m.manualReadiness(ctx, tx, prop(ctx), cid, req)
	})
}

func (m *Module) assignCartHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[CartAssignRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out, err := m.AssignCarts(ctx, tx, prop(ctx), req)
		return httpx.Page[CartAssignment]{Items: out}, err
	})
}

func (m *Module) returnCartHTTP(w http.ResponseWriter, r *http.Request) {
	aid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.ReturnCart(ctx, tx, prop(ctx), aid) })
}

func (m *Module) cartAssignmentsHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		q := r.URL.Query()
		where := "a.property_id = $1"
		args := []any{prop(ctx)}
		if v := q.Get("golfCartId"); v != "" {
			args = append(args, v)
			where += fmt.Sprintf(" AND a.golf_cart_id::text = $%d", len(args))
		}
		if v := q.Get("date"); v != "" {
			args = append(args, v)
			where += fmt.Sprintf(" AND a.play_date = $%d::date", len(args))
		}
		out, err := ListCartAssignments(ctx, tx, where, args...)
		return httpx.Page[CartAssignment]{Items: out}, err
	})
}

func (m *Module) assignLockerHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[LockerAssignRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.AssignLocker(ctx, tx, prop(ctx), req) })
}

func (m *Module) releaseLockerHTTP(w http.ResponseWriter, r *http.Request) {
	aid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.ReleaseLocker(ctx, tx, prop(ctx), aid) })
}

func (m *Module) lockerAssignmentsHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		where := "a.property_id = $1"
		args := []any{prop(ctx)}
		if v := r.URL.Query().Get("filter[status]"); v != "" {
			args = append(args, v)
			where += " AND a.status = $2"
		}
		out, err := listLockerAssignments(ctx, tx, where, args...)
		return httpx.Page[LockerAssignment]{Items: out}, err
	})
}

func (m *Module) bagDropHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[BagDropRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.DropBag(ctx, tx, prop(ctx), req) })
}

func (m *Module) collectBagHTTP(w http.ResponseWriter, r *http.Request) {
	did, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.CollectBag(ctx, tx, prop(ctx), did) })
}

func (m *Module) checkOutDeskHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		out, err := m.CheckOutDesk(ctx, tx, prop(ctx), day)
		return httpx.Page[CheckOutEntry]{Items: out}, err
	})
}

func (m *Module) checkOutHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[CheckOutRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.CheckOut(ctx, tx, prop(ctx), bid, req) })
}

func (m *Module) chargeTargetsHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		out, err := m.ChargeTargets(ctx, tx, prop(ctx), day, r.URL.Query().Get("q"))
		return httpx.Page[ChargeTarget]{Items: out}, err
	})
}

func (m *Module) bagDropsHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		day, err := dayParam(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		out, err := listBagDrops(ctx, tx, "d.property_id = $1 AND d.play_date = $2::date", prop(ctx), day.Format("2006-01-02"))
		return httpx.Page[BagDrop]{Items: out}, err
	})
}

func (m *Module) storeBagHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[BagStorageRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.StoreBag(ctx, tx, prop(ctx), req) })
}

func (m *Module) endBagHTTP(w http.ResponseWriter, r *http.Request) {
	sid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.EndBagStorage(ctx, tx, prop(ctx), sid) })
}

func (m *Module) bagStorageHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		where := "property_id = $1"
		args := []any{prop(ctx)}
		if v := r.URL.Query().Get("filter[status]"); v != "" {
			args = append(args, v)
			where += " AND status = $2"
		}
		out, err := listBagStorage(ctx, tx, where, args...)
		return httpx.Page[BagStorage]{Items: out}, err
	})
}

// ── handicaps (FR-CRS-07) ─────────────────────────────────────────────────

type HandicapRequest struct {
	CustomerID    uuid.UUID `json:"customerId"`
	HandicapIndex string    `json:"handicapIndex"`
	Source        string    `json:"source,omitempty" enum:"manual,import"`
	Notes         string    `json:"notes,omitempty"`
}

type Handicap struct {
	ID            uuid.UUID `json:"id"`
	CustomerID    uuid.UUID `json:"customerId"`
	HandicapIndex string    `json:"handicapIndex"`
	Source        string    `json:"source"`
	EffectiveAt   time.Time `json:"effectiveAt"`
	Notes         *string   `json:"notes"`
}

// RecordHandicap stores a new Handicap Index (history kept).
func RecordHandicap(ctx context.Context, tx pgx.Tx, property uuid.UUID, req HandicapRequest) (Handicap, error) {
	h, err := decimal.NewFromString(req.HandicapIndex)
	if err != nil || h.LessThan(decimal.NewFromInt(-10)) || h.GreaterThan(decimal.NewFromInt(54)) {
		return Handicap{}, errs.Validation("invalid_handicap", "handicap index must be between -10 and 54", errs.Field("handicapIndex", "invalid", "-10 … 54"))
	}
	if req.Source == "" {
		req.Source = "manual"
	}
	ok, err := customerExists(ctx, tx, property, req.CustomerID)
	if err != nil {
		return Handicap{}, err
	}
	if !ok {
		return Handicap{}, errs.Validation("customer_not_found", "customer not found", errs.Field("customerId", "not_found", "customer not found"))
	}
	hid := id.New()
	var out Handicap
	if err := tx.QueryRow(ctx, `INSERT INTO golf.handicaps (id, property_id, customer_id, handicap_index, source, notes, created_by) VALUES ($1,$2,$3,$4::numeric,$5,$6,$7)
		RETURNING id, customer_id, handicap_index::text, source, effective_at, notes`, hid, property, req.CustomerID, h.Round(1).String(), req.Source, nullStr(req.Notes),
		id.Ptr(actor(ctx))).Scan(&out.ID, &out.CustomerID, &out.HandicapIndex, &out.Source, &out.EffectiveAt, &out.Notes); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionCreate, EntityType: "golf.handicap", EntityID: hid.String(), PropertyID: &property, After: out})
}

func (m *Module) handicapHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[HandicapRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) { return RecordHandicap(ctx, tx, prop(ctx), req) })
}

func (m *Module) handicapsHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		where := "property_id = $1"
		args := []any{prop(ctx)}
		if v := r.URL.Query().Get("customerId"); v != "" {
			args = append(args, v)
			where += " AND customer_id::text = $2"
		}
		rows, err := tx.Query(ctx, `SELECT id, customer_id, handicap_index::text, source, effective_at, notes FROM golf.handicaps WHERE `+where+
			` ORDER BY effective_at DESC LIMIT 500`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Handicap{}
		for rows.Next() {
			var h Handicap
			if err := rows.Scan(&h.ID, &h.CustomerID, &h.HandicapIndex, &h.Source, &h.EffectiveAt, &h.Notes); err != nil {
				return nil, err
			}
			out = append(out, h)
		}
		return httpx.Page[Handicap]{Items: out}, rows.Err()
	})
}

func (m *Module) policiesHTTP(w http.ResponseWriter, r *http.Request) {
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out, err := CurrentPolicies(ctx, tx, prop(ctx))
		return httpx.Page[PolicyView]{Items: out}, err
	})
}

// ── offline sync actions (FR-OPS-06) ──────────────────────────────────────

// RegisterSync registers the offline actions of the Operational Staff app.
func (m *Module) RegisterSync(s *syncsvc.Service) {
	s.Handle("golf.check_in", func(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
		var req CheckInRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, errs.Validation("invalid_payload", "invalid check-in payload")
		}
		p := prop(ctx)
		if pr := authzFrom(ctx); pr == nil || !pr.Can("golf.check_in.perform", &p) {
			return nil, errs.Forbidden("missing permission golf.check_in.perform")
		}
		res, err := m.CheckIn(ctx, tx, p, req, "offline")
		if e, ok := errs.As(err); ok && e.Kind == errs.KindConflict {
			return nil, &syncsvc.ConflictError{Message: e.Message}
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"bookingId": res.Booking.ID, "checkedIn": res.CheckedIn, "flightsReady": res.FlightsReady}, nil
	})
	s.Handle("golf.bag_drop", func(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
		var req BagDropRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, errs.Validation("invalid_payload", "invalid bag drop payload")
		}
		p := prop(ctx)
		if pr := authzFrom(ctx); pr == nil || !pr.Can("golf.bag.manage", &p) {
			return nil, errs.Forbidden("missing permission golf.bag.manage")
		}
		d, err := m.DropBag(ctx, tx, p, req)
		if e, ok := errs.As(err); ok && (e.Kind == errs.KindConflict || e.Code == "tag_taken") {
			return nil, &syncsvc.ConflictError{Message: e.Message}
		}
		if err != nil {
			return nil, err
		}
		return d, nil
	})
}

// Register adds the golf routes.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{Courses, CourseSections, TeeSets, Holes, PlayingRoutes, CourseAssets, TeeSheetTemplates, Caddies, GolfCarts, Lockers} {
		eng.Register(reg, d)
	}
	add := func(rt route.Route) {
		rt.Module = "golf"
		rt.Scope = route.ScopeProperty
		reg.Add(rt)
	}
	const tc, tt, tb, tf, tcd, tgc, tk, ts = "Course", "Tee Sheet", "Bookings", "Flights", "Caddies", "Golf Carts", "Check-in", "Starter"
	// course
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/playing-routes/{id}/holes", Tag: tc, Summary: "Route holes (derived from the sections)",
		Permission: "golf.course.view", Response: RouteHoles{}, Handler: m.routeHolesHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/course-blocks", Tag: tc, Summary: "Block course availability (affected bookings returned)",
		Permission: "golf.tee_sheet.manage", Request: BlockRequest{}, Response: Block{}, Handler: m.createBlock})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/course-blocks", Tag: tc, Summary: "Course availability blocks",
		Permission: "golf.tee_sheet.view", Response: Block{}, List: true, Query: []route.Param{{Name: "courseId"}, {Name: "all", Type: "boolean"}}, Handler: m.listBlocks})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/course-blocks/{id}:cancel", Tag: tc, Summary: "Lift a course block",
		Permission: "golf.tee_sheet.manage", Response: Block{}, Status: http.StatusOK, Handler: m.cancelBlock})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/handicaps", Tag: tc, Summary: "Handicap Index history", Permission: "golf.handicap.view",
		Response: Handicap{}, List: true, Query: []route.Param{{Name: "customerId"}}, Handler: m.handicapsHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/handicaps", Tag: tc, Summary: "Record a Handicap Index", Permission: "golf.handicap.manage",
		Request: HandicapRequest{}, Response: Handicap{}, Handler: m.handicapHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/policies", Tag: tc, Summary: "Golf Policies in force (versions)", Permission: "golf.policy.view",
		Response: PolicyView{}, List: true, Handler: m.policiesHTTP})
	// tee sheet
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/tee-sheets:generate", Tag: tt, Summary: "Generate tee sheets from templates",
		Permission: "golf.tee_sheet.manage", Request: GenerateRequest{}, Response: GenerateResult{}, Status: http.StatusOK, Handler: m.generateHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/tee-times", Tag: tt, Summary: "Tee times of a day", Permission: "golf.tee_sheet.view",
		Response: Slot{}, List: true, Query: []route.Param{{Name: "date"}, {Name: "courseId"}}, Handler: m.teeTimes})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/availability", Tag: tt, Summary: "Tee Time Availability with indicative prices",
		Permission: "golf.tee_sheet.view", Response: AvailableSlot{}, List: true,
		Query: []route.Param{{Name: "date"}, {Name: "courseId"}, {Name: "session"}, {Name: "players", Type: "integer"}, {Name: "segments"}}, Handler: m.availabilityHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/tee-sheet", Tag: tt, Summary: "Tee Sheet grid (flights, players, check-in, caddy, golf cart, readiness)",
		Permission: "golf.tee_sheet.view", Response: TeeSheet{}, Query: []route.Param{{Name: "date"}, {Name: "courseId", Required: true}}, Handler: m.teeSheetHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/tee-sheet/stream", Tag: tt, Summary: "Real-time tee sheet, starter, board and course status events (SSE)",
		Permission: "golf.tee_sheet.view", RawContent: "text/event-stream", Query: []route.Param{{Name: "courseId"}, {Name: "date"}, {Name: "propertyId", Description: "Active property (EventSource cannot send X-Property-Id)"}},
		Handler: m.Hub.Stream([]string{"golf"}, "courseId", "date")})
	// holds & bookings
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/holds", Tag: tb, Summary: "Tee Hold (seats held until expiry)", Permission: "golf.booking.create",
		Request: HoldRequest{}, Response: Hold{}, Idempotent: true, Handler: m.holdHTTP})
	add(route.Route{Method: http.MethodDelete, Path: "/api/v1/golf/holds/{id}", Tag: tb, Summary: "Release a tee hold", Permission: "golf.booking.create",
		Response: Booking{}, Status: http.StatusOK, Handler: m.releaseHoldHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/bookings", Tag: tb, Summary: "Bookings", Permission: "golf.booking.view", Response: BookingSummary{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "date"}, {Name: "from"}, {Name: "to"}, {Name: "filter[status]"}, {Name: "filter[bookingType]"}, {Name: "filter[channel]"},
			{Name: "filter[courseId]"}, {Name: "filter[customerId]"}, {Name: "sort"}}, Handler: m.bookingsHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bookings", Tag: tb, Summary: "Create Booking (validation, pricing snapshot, folio, payment policy)",
		Permission: "golf.booking.create", Request: BookingRequest{}, Response: Booking{}, Idempotent: true, Handler: m.createBookingHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/bookings/{id}", Tag: tb, Summary: "Booking Detail", Permission: "golf.booking.view",
		Response: Booking{}, Handler: m.bookingHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/bookings/{id}/history", Tag: tb, Summary: "Booking History", Permission: "golf.booking.view",
		Response: HistoryEntry{}, List: true, Handler: m.historyHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bookings/{id}:confirm", Tag: tb, Summary: "Confirm a pending booking (balance paid at venue)",
		Permission: "golf.booking.update", Response: Booking{}, Status: http.StatusOK, Handler: m.confirmHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bookings/{id}:cancel", Tag: tb, Summary: "Cancel Booking (Cancellation Policy, refund, waiver)",
		Permission: "golf.booking.cancel", Request: CancelRequest{}, Response: CancelResult{}, Status: http.StatusOK, Handler: m.cancelHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bookings/{id}:reschedule", Tag: tb, Summary: "Reschedule Booking", Permission: "golf.booking.update",
		Request: RescheduleRequest{}, Response: Booking{}, Status: http.StatusOK, Handler: m.rescheduleHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bookings/{id}:no-show", Tag: tb, Summary: "Mark No-show (No-show Policy)", Permission: "golf.booking.no_show",
		Request: ReasonBody{}, Response: Booking{}, Status: http.StatusOK, Handler: m.noShowHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bookings/{id}/players", Tag: tf, Summary: "Assign Player", Permission: "golf.booking.update",
		Request: AddPlayerRequest{}, Response: Booking{}, Handler: m.addPlayerHTTP})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/golf/bookings/{id}/players/{playerId}", Tag: tf, Summary: "Edit a player (complete TBA, verify reciprocal)",
		Permission: "golf.booking.update", Request: PlayerPatch{}, Response: Booking{}, Handler: m.patchPlayerHTTP})
	add(route.Route{Method: http.MethodDelete, Path: "/api/v1/golf/bookings/{id}/players/{playerId}", Tag: tf, Summary: "Remove Player",
		Permission: "golf.booking.update", Response: Booking{}, Status: http.StatusOK, Query: []route.Param{{Name: "reason"}}, Handler: m.removePlayerHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/flights", Tag: tf, Summary: "Flights of a day", Permission: "golf.flight.view", Response: SheetFlight{}, List: true,
		Query: []route.Param{{Name: "date"}, {Name: "filter[status]"}}, Handler: m.flightsHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/flights", Tag: tf, Summary: "Create Flight", Permission: "golf.flight.manage",
		Request: CreateFlightRequest{}, Response: FlightRef{}, Handler: m.createFlightHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/flights/{id}/players", Tag: tf, Summary: "Move a player to this flight", Permission: "golf.flight.manage",
		Request: MovePlayerRequest{}, Response: Booking{}, Status: http.StatusOK, Handler: m.movePlayerHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/players", Tag: tf, Summary: "Players of a day", Permission: "golf.booking.view", Response: PlayerRow{}, List: true,
		Query: []route.Param{{Name: "date"}}, Handler: m.playersHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/rain-checks", Tag: tb, Summary: "Rain Checks", Permission: "golf.rain_check.view", Response: RainCheck{}, List: true,
		Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[customerId]"}}, Handler: m.rainChecksHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/rain-checks", Tag: tb, Summary: "Issue Rain Checks for a flight", Permission: "golf.rain_check.issue",
		Request: RainCheckRequest{}, Response: RainCheck{}, List: true, Status: http.StatusCreated, Handler: m.issueRainCheckHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/rain-checks:batch", Tag: tb, Summary: "Rain stop: issue Rain Checks for several flights",
		Permission: "golf.rain_check.issue", Request: RainCheckBatchRequest{}, Response: RainCheck{}, List: true, Status: http.StatusCreated, Handler: m.batchRainCheckHTTP})
	// check-in & starter
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/check-ins:lookup", Tag: tk, Summary: "Find today's booking by member card, QR, code or name",
		Permission: "golf.check_in.perform", Response: CheckInCandidate{}, List: true, Query: []route.Param{{Name: "method"}, {Name: "value"}, {Name: "date"}}, Handler: m.lookupHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/check-ins", Tag: tk, Summary: "Player Check-in", Permission: "golf.check_in.perform",
		Request: CheckInRequest{}, Response: CheckInResult{}, Status: http.StatusOK, Idempotent: true, Handler: m.checkInHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/starter-queue", Tag: ts, Summary: "Starter Queue (Active Dispatch Queue, on hold, dispatched)",
		Permission: "golf.starter.view", Response: StarterQueue{}, Query: []route.Param{{Name: "courseId", Required: true}, {Name: "date"}}, Handler: m.queueHTTP})
	for _, a := range []struct{ action, summary string }{{"hold", "Hold (reason required, position preserved)"}, {"skip", "Skip +1"}, {"release", "Release"},
		{"call", "Flight Call"}, {"tee-off", "Tee-Off (Round Start)"}, {"finish", "Round Finish"}} {
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/starter-queue/{flightId}:" + a.action, Tag: ts, Summary: a.summary, Permission: "golf.starter.control",
			Request: StarterAction{}, Response: StarterQueue{}, Status: http.StatusOK, Handler: m.controlHTTP(a.action)})
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/course-status", Tag: ts, Summary: "Course & Weather Status", Permission: "golf.starter.view",
		Response: CourseStatus{}, List: true, Handler: m.courseStatusListHTTP})
	add(route.Route{Method: http.MethodPut, Path: "/api/v1/golf/course-status/{courseId}", Tag: ts, Summary: "Update course, weather and night light status",
		Permission: "golf.course_status.update", Request: CourseStatusRequest{}, Response: CourseStatus{}, Handler: m.courseStatusHTTP})
	// caddy
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddy-availability", Tag: tcd, Summary: "Caddy Queue & Availability of a day",
		Permission: "golf.caddy.view", Response: CaddyBoardEntry{}, List: true, Query: []route.Param{{Name: "date"}}, Handler: m.caddyBoardHTTP})
	add(route.Route{Method: http.MethodPut, Path: "/api/v1/golf/caddy-availability", Tag: tcd, Summary: "Record Caddy Attendance",
		Permission: "golf.caddy_assignment.manage", Request: AttendanceRequest{}, Response: CaddyBoardEntry{}, List: true, Handler: m.attendanceHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-queue:reorder", Tag: tcd, Summary: "Caddy Rotation (manual order)",
		Permission: "golf.caddy_assignment.manage", Request: ReorderRequest{}, Response: CaddyBoardEntry{}, List: true, Status: http.StatusOK, Handler: m.reorderHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddy-assignments", Tag: tcd, Summary: "Caddy assignments (Current Assignment, Caddy History)",
		Permission: "golf.caddy.view", Response: CaddyAssignment{}, List: true, Query: []route.Param{{Name: "caddyId"}, {Name: "date"}, {Name: "flightId"}}, Handler: m.caddyAssignmentsHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-assignments", Tag: tcd, Summary: "Assign Caddy", Permission: "golf.caddy_assignment.manage",
		Request: CaddyAssignRequest{}, Response: CaddyAssignment{}, List: true, Status: http.StatusCreated, Handler: m.assignCaddyHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-assignments/{id}:replace", Tag: tcd, Summary: "Replace the caddy (reason recorded)",
		Permission: "golf.caddy_assignment.manage", Request: ReplaceRequest{}, Response: CaddyAssignment{}, List: true, Status: http.StatusOK, Handler: m.replaceCaddyHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-assignments/{id}:cancel", Tag: tcd, Summary: "Cancel a caddy assignment",
		Permission: "golf.caddy_assignment.manage", Request: ReasonBody{}, Response: CaddyAssignment{}, List: true, Status: http.StatusOK, Handler: m.cancelCaddyHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/caddy-tips", Tag: tcd, Summary: "Caddy tips of a day", Permission: "golf.caddy.view",
		Response: CaddyTip{}, List: true, Query: []route.Param{{Name: "date"}}, Handler: m.tipsHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/caddy-tips", Tag: tcd, Summary: "Add a Caddy Tip to the folio", Permission: "golf.check_in.perform",
		Request: TipRequest{}, Response: CaddyTip{}, Idempotent: true, Handler: m.tipHTTP})
	// check-out (FR-CHK-05) and the POS charge to a golfer's folio (FR-POS-07)
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/check-outs", Tag: tk, Summary: "Check-out desk: checked-in bookings with balance, lockers and bags",
		Permission: "golf.check_in.perform", Response: CheckOutEntry{}, List: true, Query: []route.Param{{Name: "date"}}, Handler: m.checkOutDeskHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bookings/{id}:check-out", Tag: tk, Summary: "Golfer Check-out (settle and close the folio, release lockers, hand back bags)",
		Permission: "golf.check_in.perform", Request: CheckOutRequest{}, Response: Booking{}, Status: http.StatusOK, Idempotent: true, Handler: m.checkOutHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/bookings/{id}/bill", Tag: tk, Summary: "Booking bill at the front desk: balance and the share of every player",
		Permission: "golf.booking.view", Response: BookingBill{}, Handler: m.billHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bookings/{id}/bill:pay", Tag: tk, Summary: "Pay the bill in full, in part or per player (split bill)",
		Permission: "billing.payment.create", Request: BillPayRequest{}, Response: BookingBill{}, Status: http.StatusOK, Idempotent: true, Handler: m.payBillHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bills:pay-combined", Tag: tk, Summary: "Merge several bookings into one bill and pay it with one tender",
		Permission: "billing.payment.create", Request: CombinedPayRequest{}, Response: BookingBill{}, List: true, Status: http.StatusOK, Idempotent: true,
		Handler: m.payCombinedHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/charge-targets", Tag: tk, Summary: "Golfers whose booking folio a POS order can be charged to",
		Permission: "commercial.order.pay", Response: ChargeTarget{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "date"}}, Handler: m.chargeTargetsHTTP})
	// golf carts
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/golf-cart-board", Tag: tgc, Summary: "Golf Cart Readiness board", Permission: "golf.golf_cart.view",
		Response: CartBoardEntry{}, List: true, Query: []route.Param{{Name: "date"}}, Handler: m.cartBoardHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/golf-carts/{id}:set-readiness", Tag: tgc, Summary: "Set Golf Cart Readiness (reason for Maintenance / Out of Service)",
		Permission: "golf.golf_cart.update", Request: ReadinessRequest{}, Response: CartBoardEntry{}, Status: http.StatusOK, Handler: m.readinessHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/golf-cart-assignments", Tag: tgc, Summary: "Golf cart usage history", Permission: "golf.golf_cart.view",
		Response: CartAssignment{}, List: true, Query: []route.Param{{Name: "golfCartId"}, {Name: "date"}}, Handler: m.cartAssignmentsHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/golf-cart-assignments", Tag: tgc, Summary: "Assign Golf Cart (sharing rule, surcharge)",
		Permission: "golf.golf_cart_assignment.manage", Request: CartAssignRequest{}, Response: CartAssignment{}, List: true, Status: http.StatusCreated, Handler: m.assignCartHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/golf-cart-assignments/{id}:return", Tag: tgc, Summary: "Return a golf cart",
		Permission: "golf.golf_cart_assignment.manage", Response: CartAssignment{}, Status: http.StatusOK, Handler: m.returnCartHTTP})
	// lockers & bags
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/locker-assignments", Tag: tk, Summary: "Locker assignments", Permission: "golf.locker.view",
		Response: LockerAssignment{}, List: true, Query: []route.Param{{Name: "filter[status]"}}, Handler: m.lockerAssignmentsHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/locker-assignments", Tag: tk, Summary: "Locker Assignment (daily or periodic)",
		Permission: "golf.locker_assignment.manage", Request: LockerAssignRequest{}, Response: LockerAssignment{}, Handler: m.assignLockerHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/locker-assignments/{id}:release", Tag: tk, Summary: "Release a locker",
		Permission: "golf.locker_assignment.manage", Response: LockerAssignment{}, Status: http.StatusOK, Handler: m.releaseLockerHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/bag-drops", Tag: tk, Summary: "Bag drops of a day", Permission: "golf.booking.view",
		Response: BagDrop{}, List: true, Query: []route.Param{{Name: "date"}}, Handler: m.bagDropsHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bag-drops", Tag: tk, Summary: "Bag Drop (tag, bags, time)", Permission: "golf.bag.manage",
		Request: BagDropRequest{}, Response: BagDrop{}, Idempotent: true, Handler: m.bagDropHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bag-drops/{id}:collect", Tag: tk, Summary: "Bag collected", Permission: "golf.bag.manage",
		Response: BagDrop{}, Status: http.StatusOK, Handler: m.collectBagHTTP})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/bag-storage", Tag: tk, Summary: "Bag Storage", Permission: "golf.booking.view",
		Response: BagStorage{}, List: true, Query: []route.Param{{Name: "filter[status]"}}, Handler: m.bagStorageHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bag-storage", Tag: tk, Summary: "Store a bag (rack, period, fee)", Permission: "golf.bag.manage",
		Request: BagStorageRequest{}, Response: BagStorage{}, Handler: m.storeBagHTTP})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/golf/bag-storage/{id}:end", Tag: tk, Summary: "End a bag storage", Permission: "golf.bag.manage",
		Response: BagStorage{}, Status: http.StatusOK, Handler: m.endBagHTTP})
	m.registerPortal(reg)
	m.registerMemberJourney(reg)
}
