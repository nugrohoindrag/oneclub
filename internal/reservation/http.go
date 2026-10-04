package reservation

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/resource"
)

// ResourceTypes is the registry (instance-wide master, FR-RSV-01).
var ResourceTypes = &resource.Def{
	Key: "reservation.resource_type", Module: "reservation", Perm: "reservation.resource_type", Path: "/api/v1/reservation/resource-types",
	Table: "reservation.resource_types", Name: "Resource Type", Plural: "Resource Types", Tag: "Booking", Archive: true, CodeField: "code",
	OrderBy: "sort_order, name", NoDelete: true,
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 40, CreateOnly: true, Search: true},
		resource.Name(),
		{Name: "businessLine", Column: "business_line", Label: "Business Line", Kind: resource.Enum, Enum: []string{"golf", "sportclub", "stay", "banquet", "other"}, Default: "other", Filter: true},
		{Name: "reservationModel", Column: "reservation_model", Label: "Reservation Model", Kind: resource.Enum, Required: true,
			Enum: []string{"time_slot", "entry", "class_session", "night", "day_use", "block", "package", "quantity", "bay", "tee_time"}},
		{Name: "allocationMode", Column: "allocation_mode", Label: "Allocation Mode", Kind: resource.Enum, Enum: []string{"exclusive", "capacity"}, Required: true, CreateOnly: true},
		{Name: "slotMinutes", Column: "slot_minutes", Label: "Slot Length (minutes)", Kind: resource.Int, Default: int64(60), Min: resource.Min(1)},
		{Name: "bufferBeforeMinutes", Column: "buffer_before_minutes", Label: "Buffer Before (minutes)", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "bufferAfterMinutes", Column: "buffer_after_minutes", Label: "Buffer After (minutes)", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "holdMinutes", Column: "hold_minutes", Label: "Hold Duration (minutes)", Kind: resource.Int, Default: int64(15), Min: resource.Min(1)},
		{Name: "openTime", Column: "open_time", Label: "Opening Time", Kind: resource.Time, Default: "07:00"},
		{Name: "closeTime", Column: "close_time", Label: "Closing Time", Kind: resource.Time, Default: "21:00"},
		{Name: "checkInTime", Column: "check_in_time", Label: "Check-in Time", Kind: resource.Time},
		{Name: "checkOutTime", Column: "check_out_time", Label: "Check-out Time", Kind: resource.Time},
		{Name: "serviceType", Column: "service_type", Label: "Pricing Service Type", Kind: resource.Enum, Enum: commercial.ServiceTypes},
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(0)},
		resource.Status("active", "inactive"),
	},
}

// Slot is one availability slot (FR-RSV-03).
type Slot struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Status    string    `json:"status" enum:"available,reserved,full,blocked,closed"`
	Capacity  *int      `json:"capacity"`
	Remaining *int      `json:"remaining"`
	Price     *string   `json:"price" doc:"Indicative price for the segment (when requested)"`
}

// ResourceAvailability lists the slots of one resource.
type ResourceAvailability struct {
	Resource Resource `json:"resource"`
	Slots    []Slot   `json:"slots"`
}

// Availability is the response of GET /reservation/availability.
type Availability struct {
	ResourceType ResourceType           `json:"resourceType"`
	From         time.Time              `json:"from"`
	To           time.Time              `json:"to"`
	Resources    []ResourceAvailability `json:"resources"`
}

// AvailabilityQuery parameters.
type AvailabilityQuery struct {
	ResourceType string
	Date         time.Time // local date
	Days         int
	ResourceID   *uuid.UUID
	ResourceIDs  []uuid.UUID
	Segment      string
	WithPrice    bool
}

func clockOn(day time.Time, hhmm string, loc *time.Location) time.Time {
	t, _ := time.Parse("15:04", hhmm)
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, loc)
}

// Availability builds the slot grid with status, remaining capacity and an
// optional indicative price.
func (e *Engine) Availability(ctx context.Context, q dbtx.Querier, property uuid.UUID, aq AvailabilityQuery) (Availability, error) {
	rt, err := e.Type(ctx, q, aq.ResourceType)
	if err != nil {
		return Availability{}, err
	}
	if aq.Days <= 0 {
		aq.Days = 1
	}
	if aq.Days > 31 {
		aq.Days = 31
	}
	loc := calendar.Location(ctx, q)
	day0 := time.Date(aq.Date.Year(), aq.Date.Month(), aq.Date.Day(), 0, 0, 0, 0, loc)
	from, to := day0, day0.AddDate(0, 0, aq.Days)
	args := []any{property, rt.Code}
	filter := ""
	if aq.ResourceID != nil {
		args = append(args, *aq.ResourceID)
		filter = " AND id = $3"
	} else if len(aq.ResourceIDs) > 0 {
		args = append(args, aq.ResourceIDs)
		filter = " AND id = ANY($3)"
	}
	resources, err := handle.List[Resource](q.Query(ctx, resourceSelect+` WHERE property_id = $1 AND resource_type = $2 AND status = 'active'
		AND archived_at IS NULL`+filter+` ORDER BY name`, args...))
	if err != nil {
		return Availability{}, err
	}
	out := Availability{ResourceType: rt, From: from, To: to, Resources: []ResourceAvailability{}}
	now := clock.Now()
	for _, res := range resources {
		var grid [][2]time.Time
		for d := 0; d < aq.Days; d++ {
			day := day0.AddDate(0, 0, d)
			kinds, err := calendar.Kinds(ctx, q, property, day)
			if err != nil {
				return out, err
			}
			if _, closed := kinds["closed"]; closed {
				continue
			}
			switch {
			case rt.ReservationModel == "night":
				ci, co := "14:00", "12:00"
				if rt.CheckInTime != nil {
					ci = *rt.CheckInTime
				}
				if rt.CheckOutTime != nil {
					co = *rt.CheckOutTime
				}
				grid = append(grid, [2]time.Time{clockOn(day, ci, loc), clockOn(day.AddDate(0, 0, 1), co, loc)})
			case rt.ReservationModel == "class_session":
				// sessions are the slots
			case rt.SlotMinutes >= 1440:
				grid = append(grid, [2]time.Time{day, day.AddDate(0, 0, 1)})
			default:
				open, close := clockOn(day, rt.OpenTime, loc), clockOn(day, rt.CloseTime, loc)
				step := time.Duration(rt.SlotMinutes) * time.Minute
				for t := open; !t.Add(step).After(close); t = t.Add(step) {
					grid = append(grid, [2]time.Time{t, t.Add(step)})
				}
			}
		}
		ra := ResourceAvailability{Resource: res, Slots: []Slot{}}
		if rt.AllocationMode == "exclusive" {
			type busy struct {
				Start time.Time `db:"start_at"`
				End   time.Time `db:"end_at"`
				Kind  string    `db:"kind"`
			}
			bs, err := handle.List[busy](q.Query(ctx, `SELECT lower(a.period) AS start_at, upper(a.period) AS end_at, coalesce(r.kind, 'booking') AS kind
				FROM reservation.allocations a LEFT JOIN reservation.reservations r ON r.id = a.reservation_id
				WHERE a.resource_id = $1 AND a.status IN ('held', 'confirmed') AND a.period && tstzrange($2, $3, '[)')`, res.ID, from, to.Add(48*time.Hour)))
			if err != nil {
				return out, err
			}
			for _, g := range grid {
				s := Slot{Start: g[0], End: g[1], Status: "available"}
				for _, b := range bs {
					if b.Start.Before(g[1]) && g[0].Before(b.End) {
						s.Status = "reserved"
						if b.Kind == "block" {
							s.Status = "blocked"
						}
						break
					}
				}
				if s.Status == "available" && g[1].Before(now) {
					s.Status = "closed"
				}
				ra.Slots = append(ra.Slots, s)
			}
		} else {
			type cs struct {
				Start    time.Time `db:"start_at"`
				End      time.Time `db:"end_at"`
				Capacity int       `db:"capacity"`
				Booked   int       `db:"booked"`
				Status   string    `db:"status"`
			}
			slots, err := handle.List[cs](q.Query(ctx, `SELECT lower(period) AS start_at, upper(period) AS end_at, capacity, booked, status
				FROM reservation.capacity_slots WHERE resource_id = $1 AND period && tstzrange($2, $3, '[)') ORDER BY lower(period)`, res.ID, from, to))
			if err != nil {
				return out, err
			}
			if rt.ReservationModel == "class_session" {
				for _, c := range slots {
					grid = append(grid, [2]time.Time{c.Start, c.End})
				}
			}
			for _, g := range grid {
				s := Slot{Start: g[0], End: g[1], Status: "available", Capacity: res.Capacity}
				rem := 0
				if res.Capacity != nil {
					rem = *res.Capacity
				}
				for _, c := range slots {
					if c.Start.Before(g[1]) && g[0].Before(c.End) {
						capv := c.Capacity
						s.Capacity = &capv
						if c.Capacity-c.Booked < rem || c.Start.Equal(g[0]) {
							rem = c.Capacity - c.Booked
						}
						if c.Status == "closed" {
							s.Status = "blocked"
						}
					}
				}
				s.Remaining = &rem
				if s.Status == "available" && rem <= 0 {
					s.Status = "full"
				}
				if s.Status == "available" && g[1].Before(now) {
					s.Status = "closed"
				}
				ra.Slots = append(ra.Slots, s)
			}
		}
		if aq.WithPrice && e.Price != nil {
			for i := range ra.Slots {
				if ra.Slots[i].Status != "available" {
					continue
				}
				p, err := e.Price(ctx, q, property, rt, res, ra.Slots[i].Start, ra.Slots[i].End, aq.Segment)
				if err == nil {
					ra.Slots[i].Price = p
				}
			}
		}
		out.Resources = append(out.Resources, ra)
	}
	return out, nil
}

// CalendarEntry is one booking on the Booking Calendar (FR-RSV-10).
type CalendarEntry struct {
	ReservationID uuid.UUID `json:"reservationId" db:"reservation_id"`
	LineID        uuid.UUID `json:"lineId" db:"line_id"`
	Code          string    `json:"code" db:"code"`
	Kind          string    `json:"kind" db:"kind"`
	Status        string    `json:"status" db:"status"`
	BusinessLine  string    `json:"businessLine" db:"business_line"`
	Start         time.Time `json:"start" db:"start_at"`
	End           time.Time `json:"end" db:"end_at"`
	Quantity      int       `json:"quantity" db:"quantity"`
	Customer      *string   `json:"customer" db:"customer"`
}

// CalendarRow is one resource row on the timeline.
type CalendarRow struct {
	Resource Resource        `json:"resource"`
	Entries  []CalendarEntry `json:"entries"`
}

// Calendar lists bookings per resource in a period.
func (e *Engine) Calendar(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, types []string, venue *uuid.UUID) ([]CalendarRow, error) {
	resources, err := handle.List[Resource](q.Query(ctx, resourceSelect+` WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND (cardinality($2::text[]) = 0 OR resource_type = ANY($2)) AND ($3::uuid IS NULL OR venue_id = $3) ORDER BY resource_type, name`, property, types, venue))
	if err != nil {
		return nil, err
	}
	entries, err := handle.List[struct {
		ResourceID uuid.UUID `db:"resource_id"`
		CalendarEntry
	}](q.Query(ctx, `SELECT l.resource_id, r.id AS reservation_id, l.id AS line_id, r.code, r.kind, r.status, r.business_line,
		lower(l.period) AS start_at, upper(l.period) AS end_at, l.quantity, coalesce(c.name, r.guest_name, r.corporate_name) AS customer
		FROM reservation.reservation_lines l JOIN reservation.reservations r ON r.id = l.reservation_id LEFT JOIN crm.customers c ON c.id = r.customer_id
		WHERE r.property_id = $1 AND l.period && tstzrange($2, $3, '[)') AND r.status NOT IN ('cancelled', 'expired', 'no_show')
		ORDER BY lower(l.period)`, property, from, to))
	if err != nil {
		return nil, err
	}
	out := []CalendarRow{}
	for _, res := range resources {
		row := CalendarRow{Resource: res, Entries: []CalendarEntry{}}
		for _, en := range entries {
			if en.ResourceID == res.ID {
				row.Entries = append(row.Entries, en.CalendarEntry)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// ChargeLines prices every line with Commercial pricing (resource type
// service type, resource price item, segment), opens the reservation folio
// and posts the charges (C1), and sets the deposit required by policy.
func (e *Engine) ChargeLines(ctx context.Context, tx pgx.Tx, r Reservation, segment string, quantity int) (Reservation, decimal.Decimal, error) {
	total := decimal.Zero
	folioID := r.FolioID
	if folioID == nil {
		f, err := e.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: r.PropertyID, CustomerID: r.CustomerID, HolderName: deref(r.GuestName), SourceType: "reservation", SourceID: &r.ID}, BusinessLine: lineOf(r.BusinessLine), ReservationID: &r.ID, CorporateName: deref(r.CorporateName)})
		if err != nil {
			return r, total, err
		}
		folioID = &f.ID
		if err := e.SetFolio(ctx, tx, r.ID, f.ID); err != nil {
			return r, total, err
		}
	}
	for _, l := range r.Lines {
		rt, err := e.Type(ctx, tx, l.ResourceType)
		if err != nil {
			return r, total, err
		}
		if rt.ServiceType == nil {
			continue
		}
		res, err := e.Resource(ctx, tx, l.ResourceID)
		if err != nil {
			return r, total, err
		}
		qty := l.Quantity
		if quantity > 0 {
			qty = quantity
		}
		end := l.End
		pr, err := commercial.Pricer{}.Price(ctx, tx, r.PropertyID, commercial.PriceRequest{ServiceType: *rt.ServiceType, ItemRef: res.PriceItem(),
			Segment: segment, Start: l.Start, End: &end, Quantity: qty, Channel: r.Channel})
		if err != nil {
			return r, total, fmt.Errorf("line %d: %w", l.LineNo, err)
		}
		if _, err := e.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *folioID, ReferenceType: "reservation.line", ReferenceID: &l.ID, Description: res.Name + " · " + l.Start.In(calendar.Location(ctx, tx)).Format("2 Jan 15:04"), Quantity: decimal.RequireFromString(pr.Units), Net: pr.Net(), Service: pr.ServiceAmount(), Tax: pr.TaxAmount(), SnapshotID: pr.SnapshotID}, BusinessLine: lineOf(r.BusinessLine), RevenueComponent: pr.RevenueComponent, TaxLines: pr.Tax.Lines}); err != nil {
			return r, total, err
		}
		if err := e.SetLinePrice(ctx, tx, l.ID, pr.SnapshotID, pr.Total()); err != nil {
			return r, total, err
		}
		total = total.Add(pr.Total())
	}
	if len(r.Lines) > 0 {
		dp, ref, _, _, err := e.Policies(ctx, tx, r.PropertyID, r.Lines[0].ResourceType)
		if err != nil {
			return r, total, err
		}
		if dep := dp.DepositFor(total); dep.IsPositive() {
			due := clock.Now().Add(time.Duration(dp.DueHours) * time.Hour)
			if err := e.SetDeposit(ctx, tx, r.ID, dep, &due); err != nil {
				return r, total, err
			}
			if _, err := tx.Exec(ctx, `UPDATE reservation.reservations SET policy_refs = policy_refs || $2::jsonb WHERE id = $1`, r.ID,
				fmt.Sprintf(`[{"code":%q,"version":%d}]`, ref.Code, ref.Version)); err != nil {
				return r, total, err
			}
		}
	}
	out, err := e.Get(ctx, tx, r.ID, false)
	return out, total, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func lineOf(businessLine string) string {
	switch businessLine {
	case "golf", "sportclub", "stay":
		return businessLine
	}
	return billing.LineOther
}

// RecurringResult is the outcome per occurrence (FR-RSV-11).
type RecurringResult struct {
	Date          string     `json:"date"`
	Status        string     `json:"status" enum:"created,conflict"`
	ReservationID *uuid.UUID `json:"reservationId"`
	Code          string     `json:"code,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// BookRecurring books every occurrence in its own savepoint so that a
// conflict on one date does not cancel the others.
func (e *Engine) BookRecurring(ctx context.Context, tx pgx.Tx, property uuid.UUID, req BookRequest, intervalDays, occurrences int, segment string, charge bool) ([]RecurringResult, error) {
	if intervalDays <= 0 {
		intervalDays = 7
	}
	if occurrences <= 0 || occurrences > 52 {
		return nil, handle.Invalid("occurrences", "invalid_occurrences", "occurrences must be between 1 and 52")
	}
	loc := calendar.Location(ctx, tx)
	group := uuid.New()
	req.RecurringGroupID = &group
	out := []RecurringResult{}
	for i := 0; i < occurrences; i++ {
		occ := req
		occ.Lines = make([]LineRequest, len(req.Lines))
		for j, l := range req.Lines {
			s, en := l.Start.In(loc), l.End.In(loc)
			l.Start = time.Date(s.Year(), s.Month(), s.Day()+i*intervalDays, s.Hour(), s.Minute(), 0, 0, loc)
			l.End = time.Date(en.Year(), en.Month(), en.Day()+i*intervalDays, en.Hour(), en.Minute(), 0, 0, loc)
			occ.Lines[j] = l
		}
		res := RecurringResult{Date: occ.Lines[0].Start.In(loc).Format("2006-01-02")}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return out, err
		}
		r, err := e.Book(ctx, sp, property, occ)
		if err == nil && charge {
			_, _, err = e.ChargeLines(ctx, sp, r, segment, 0)
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
				res.Status, res.Error = "conflict", de.Message
				out = append(out, res)
				continue
			}
			return out, err
		}
		if err := sp.Commit(ctx); err != nil {
			return out, err
		}
		res.Status, res.ReservationID, res.Code = "created", &r.ID, r.Code
		out = append(out, res)
	}
	return out, nil
}

// ── HTTP ──────────────────────────────────────────────────────────────────

// BookInput is the generic booking body (Booking → Calendar / Back Office).
type BookInput struct {
	Lines         []LineRequest `json:"lines"`
	CustomerID    *uuid.UUID    `json:"customerId,omitempty"`
	GuestName     string        `json:"guestName,omitempty"`
	GuestPhone    string        `json:"guestPhone,omitempty"`
	GuestEmail    string        `json:"guestEmail,omitempty"`
	CorporateName string        `json:"corporateName,omitempty"`
	Channel       string        `json:"channel,omitempty" enum:"member_app,website,back_office,walk_in,import,ops"`
	Notes         string        `json:"notes,omitempty"`
	Confirm       bool          `json:"confirm,omitempty" doc:"Create as Confirmed instead of Pending"`
	Charge        bool          `json:"charge,omitempty" doc:"Price every line and post charges to a reservation folio"`
	Segment       string        `json:"segment,omitempty"`
}

func (in BookInput) request(hold bool) BookRequest {
	return BookRequest{Lines: in.Lines, CustomerID: in.CustomerID, GuestName: in.GuestName, GuestPhone: in.GuestPhone, GuestEmail: in.GuestEmail,
		CorporateName: in.CorporateName, Channel: in.Channel, Notes: in.Notes, Confirm: in.Confirm && !hold, Hold: hold}
}

type RecurringInput struct {
	BookInput
	IntervalDays int `json:"intervalDays,omitempty" doc:"Default 7 (weekly)"`
	Occurrences  int `json:"occurrences"`
}

type CancelInput struct {
	Reason   string `json:"reason"`
	WaiveFee bool   `json:"waiveFee,omitempty"`
}

type ConfirmInput struct {
	Force bool `json:"force,omitempty" doc:"Confirm without the deposit required by policy (permission reservation.reservation.override)"`
}

type RescheduleInput struct {
	Lines  []LineChange `json:"lines"`
	Reason string       `json:"reason,omitempty"`
}

type BlockInput struct {
	ResourceID uuid.UUID `json:"resourceId"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Reason     string    `json:"reason" enum:"maintenance,private_event,management_hold,weather_closure,tournament,other"`
	Notes      string    `json:"notes,omitempty"`
}

type ReasonBody struct {
	Reason string `json:"reason,omitempty"`
}

// ReservationList is a lighter list row.
type ReservationList = Reservation

// Register adds the reservation routes.
func (e *Engine) Register(reg *route.Registry, eng *resource.Engine) {
	e.registerMe(reg)
	e.registerPublic(reg)
	eng.Register(reg, ResourceTypes)
	db := e.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag = "reservation", "Booking"
		if rt.Scope == route.ScopeGlobal {
			rt.Scope = route.ScopeProperty
		}
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reservation/availability", Summary: "Availability per resource type and date (status, remaining capacity, indicative price)",
		Permission: "reservation.reservation.view", Response: Availability{},
		Query: []route.Param{{Name: "resourceType", Required: true}, {Name: "date", Description: "YYYY-MM-DD (local)"}, {Name: "days", Type: "integer"},
			{Name: "resourceId"}, {Name: "segment"}, {Name: "withPrice", Type: "boolean"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Availability, error) {
			aq, err := availabilityQuery(ctx, tx, r)
			if err != nil {
				return Availability{}, err
			}
			return e.Availability(ctx, tx, handle.Property(ctx), aq)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reservation/calendar", Summary: "Booking Calendar across business lines (timeline per resource)",
		Permission: "reservation.reservation.view", Response: CalendarRow{}, List: true,
		Query: []route.Param{{Name: "from", Description: "YYYY-MM-DD"}, {Name: "days", Type: "integer"}, {Name: "resourceType", Description: "Comma separated"}, {Name: "venueId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CalendarRow], error) {
			loc := calendar.Location(ctx, tx)
			n := clock.Now().In(loc)
			d, err := handle.QueryDate(r, "from", time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[CalendarRow]{}, err
			}
			from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
			days := min(max(handle.QueryInt(r, "days", 1), 1), 31)
			venue, err := handle.QueryUUID(r, "venueId")
			if err != nil {
				return httpx.Page[CalendarRow]{}, err
			}
			var types []string
			for _, t := range splitComma(r.URL.Query().Get("resourceType")) {
				types = append(types, t)
			}
			if types == nil {
				types = []string{}
			}
			return handle.Page(e.Calendar(ctx, tx, handle.Property(ctx), from, from.AddDate(0, 0, days), types, venue))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/reservation/holds", Summary: "Place a hold (Draft) on one or more resources",
		Permission: "reservation.reservation.create", Request: BookInput{}, Response: Reservation{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in BookInput) (Reservation, error) {
			res, err := e.Book(ctx, tx, handle.Property(ctx), in.request(true))
			if err == nil && in.Charge {
				res, _, err = e.ChargeLines(ctx, tx, res, in.Segment, 0)
			}
			return res, err
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/reservation/reservations", Summary: "Create reservation (multi-line, all or nothing)",
		Permission: "reservation.reservation.create", Request: BookInput{}, Response: Reservation{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in BookInput) (Reservation, error) {
			res, err := e.Book(ctx, tx, handle.Property(ctx), in.request(false))
			if err == nil && in.Charge {
				res, _, err = e.ChargeLines(ctx, tx, res, in.Segment, 0)
			}
			return res, err
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/reservation/reservations:recurring", Summary: "Recurring booking (e.g. every Tuesday for 8 weeks) with result per date",
		Permission: "reservation.reservation.create", Request: RecurringInput{}, Response: RecurringResult{}, List: true, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RecurringInput) (httpx.Page[RecurringResult], error) {
			return handle.Page(e.BookRecurring(ctx, tx, handle.Property(ctx), in.request(false), in.IntervalDays, in.Occurrences, in.Segment, in.Charge))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/reservation/blocks", Summary: "Block a resource (maintenance, private event, management hold)",
		Permission: "reservation.block.manage", Request: BlockInput{}, Response: Reservation{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in BlockInput) (Reservation, error) {
			if in.Reason == "" {
				return Reservation{}, handle.Invalid("reason", "required", "reason is required")
			}
			return e.Book(ctx, tx, handle.Property(ctx), BookRequest{Kind: "block", Lines: []LineRequest{{ResourceID: in.ResourceID, Start: in.Start, End: in.End,
				Description: "Blocked: " + in.Reason}}, Confirm: true, Notes: in.Notes, Attributes: map[string]any{"blockReason": in.Reason}})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reservation/reservations", Summary: "List reservations", Permission: "reservation.reservation.view",
		Response: Reservation{}, List: true,
		Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[businessLine]"}, {Name: "filter[customerId]"}, {Name: "filter[kind]"}, {Name: "q"}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Reservation], error) {
			lp := httpx.ParseList(r)
			from, err := handle.QueryTime(r, "from", time.Time{})
			if err != nil {
				return httpx.Page[Reservation]{}, err
			}
			to, err := handle.QueryTime(r, "to", time.Time{})
			if err != nil {
				return httpx.Page[Reservation]{}, err
			}
			list, err := handle.List[Reservation](tx.Query(ctx, reservationSelect+` WHERE r.property_id = $1
				AND ($2 = '' OR r.status = ANY(string_to_array($2, ','))) AND ($3 = '' OR r.business_line = $3) AND ($4 = '' OR r.customer_id::text = $4)
				AND ($5 = '' OR r.kind = $5) AND ($6 = '' OR r.code ILIKE '%' || $6 || '%' OR c.name ILIKE '%' || $6 || '%' OR r.guest_name ILIKE '%' || $6 || '%')
				AND ($7::timestamptz = '0001-01-01T00:00:00Z' OR EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND upper(l.period) > $7))
				AND ($8::timestamptz = '0001-01-01T00:00:00Z' OR EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND lower(l.period) < $8))
				ORDER BY r.created_at DESC LIMIT $9`, handle.Property(ctx), lp.Filters["status"], lp.Filters["businessLine"], lp.Filters["customerId"],
				lp.Filters["kind"], lp.Q, from, to, lp.Limit))
			if err != nil {
				return httpx.Page[Reservation]{}, err
			}
			for i := range list {
				if list[i].Lines, err = handle.List[Line](tx.Query(ctx, lineSelect+` WHERE l.reservation_id = $1 ORDER BY l.line_no`, list[i].ID)); err != nil {
					return httpx.Page[Reservation]{}, err
				}
			}
			return handle.Page(list, nil)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reservation/reservations/{id}", Summary: "View reservation with lines and history",
		Permission: "reservation.reservation.view", Response: Reservation{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Reservation, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return Reservation{}, err
			}
			return e.Get(ctx, tx, rid, true)
		})})
	action := func(path, summary, perm string, fn func(ctx context.Context, tx pgx.Tx, rid uuid.UUID, r *http.Request) (Reservation, error)) {
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/reservation/reservations/{id}:" + path, Summary: summary, Permission: perm,
			Request: ReasonBody{}, Response: Reservation{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonBody) (Reservation, error) {
				rid, err := handle.ID(r)
				if err != nil {
					return Reservation{}, err
				}
				r = r.WithContext(context.WithValue(ctx, reasonKey{}, in.Reason))
				return fn(ctx, tx, rid, r)
			})})
	}
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/reservation/reservations/{id}:confirm", Summary: "Confirm reservation",
		Permission: "reservation.reservation.update", Request: ConfirmInput{}, Response: Reservation{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ConfirmInput) (Reservation, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return Reservation{}, err
			}
			if in.Force {
				if err := requirePerm(ctx, "reservation.reservation.override"); err != nil {
					return Reservation{}, err
				}
			}
			return e.Confirm(ctx, tx, rid, in.Force)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/reservation/reservations/{id}:cancel", Summary: "Cancel reservation (Cancellation / Refund Policy)",
		Permission: "reservation.reservation.cancel", Request: CancelInput{}, Response: CancelResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CancelInput) (CancelResult, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return CancelResult{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return CancelResult{}, err
			}
			if in.WaiveFee {
				if err := requirePerm(ctx, "reservation.reservation.override"); err != nil {
					return CancelResult{}, err
				}
			}
			return e.Cancel(ctx, tx, rid, in.Reason, in.WaiveFee)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/reservation/reservations/{id}:reschedule", Summary: "Reschedule lines (new time and/or resource)",
		Permission: "reservation.reservation.update", Request: RescheduleInput{}, Response: Reservation{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RescheduleInput) (Reservation, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return Reservation{}, err
			}
			return e.Reschedule(ctx, tx, rid, in.Lines, in.Reason)
		})})
	action("check-in", "Check-in reservation", "reservation.reservation.update", func(ctx context.Context, tx pgx.Tx, rid uuid.UUID, _ *http.Request) (Reservation, error) {
		return e.CheckIn(ctx, tx, rid)
	})
	action("no-show", "Mark No-show", "reservation.reservation.update", func(ctx context.Context, tx pgx.Tx, rid uuid.UUID, r *http.Request) (Reservation, error) {
		reason, _ := r.Context().Value(reasonKey{}).(string)
		return e.NoShow(ctx, tx, rid, reason)
	})
	action("complete", "Complete reservation", "reservation.reservation.update", func(ctx context.Context, tx pgx.Tx, rid uuid.UUID, _ *http.Request) (Reservation, error) {
		return e.Complete(ctx, tx, rid)
	})
}

type reasonKey struct{}

func requirePerm(ctx context.Context, perm string) error {
	pid := handle.Property(ctx)
	return authz.RequireAt(ctx, perm, pid)
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func availabilityQuery(ctx context.Context, q dbtx.Querier, r *http.Request) (AvailabilityQuery, error) {
	loc := calendar.Location(ctx, q)
	n := clock.Now().In(loc)
	d, err := handle.QueryDate(r, "date", time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC))
	if err != nil {
		return AvailabilityQuery{}, err
	}
	rid, err := handle.QueryUUID(r, "resourceId")
	if err != nil {
		return AvailabilityQuery{}, err
	}
	rt := r.URL.Query().Get("resourceType")
	if rt == "" {
		return AvailabilityQuery{}, errs.BadRequest("resource_type_required", "resourceType is required")
	}
	return AvailabilityQuery{ResourceType: rt, Date: d, Days: handle.QueryInt(r, "days", 1), ResourceID: rid,
		Segment: r.URL.Query().Get("segment"), WithPrice: r.URL.Query().Get("withPrice") == "true"}, nil
}

// ── jobs ──────────────────────────────────────────────────────────────────

// ExpireHoldsArgs expires Draft reservations whose hold passed.
type ExpireHoldsArgs struct{}

func (ExpireHoldsArgs) Kind() string { return "reservation_expire_holds" }

func (ExpireHoldsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// ExpireHoldsWorker runs every minute.
type ExpireHoldsWorker struct {
	river.WorkerDefaults[ExpireHoldsArgs]
	DB *dbtx.DB
}

func (w *ExpireHoldsWorker) Work(ctx context.Context, _ *river.Job[ExpireHoldsArgs]) error {
	_, err := ExpireHolds(ctx, w.DB)
	return err
}

// RegisterEngineJobs adds the P2 hold expiry job.
func RegisterEngineJobs(reg *jobs.Registrar, db *dbtx.DB) {
	river.AddWorker(reg.Workers, &ExpireHoldsWorker{DB: db})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return ExpireHoldsArgs{}, nil }, nil))
}
