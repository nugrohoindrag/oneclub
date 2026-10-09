package stay

// Waitlist (requirements §30): when a room type is full the guest joins the
// waitlist with the preferred date, nights and guests. When a bungalow of the
// type frees up for those nights (a cancellation, a no-show, an early
// departure, a released block) the entry is Offered and the guest and the
// reservation team are notified; staff convert it into a reservation once
// the guest confirms.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// WaitlistEntry is a guest waiting for a room type.
type WaitlistEntry struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	WaitlistNo     string     `json:"waitlistNo" db:"waitlist_no"`
	CustomerID     *uuid.UUID `json:"customerId" db:"customer_id"`
	GuestName      string     `json:"guestName" db:"guest_name"`
	GuestPhone     *string    `json:"guestPhone" db:"guest_phone"`
	GuestEmail     *string    `json:"guestEmail" db:"guest_email"`
	BungalowTypeID uuid.UUID  `json:"bungalowTypeId" db:"bungalow_type_id"`
	TypeName       string     `json:"typeName" db:"type_name"`
	ArrivalDate    string     `json:"arrivalDate" db:"arrival_date"`
	Nights         int        `json:"nights" db:"nights"`
	Adults         int        `json:"adults" db:"adults"`
	Children       int        `json:"children" db:"children"`
	RatePlanCode   *string    `json:"ratePlanCode" db:"rate_plan_code"`
	Priority       string     `json:"priority" db:"priority" enum:"low,normal,high,vip"`
	BookingSource  string     `json:"bookingSource" db:"booking_source"`
	Status         string     `json:"status" db:"status" enum:"waiting,offered,converted,cancelled,expired"`
	OfferedAt      *time.Time `json:"offeredAt" db:"offered_at"`
	StayID         *uuid.UUID `json:"stayId" db:"stay_id"`
	StayNo         *string    `json:"stayNo" db:"stay_no"`
	Notes          *string    `json:"notes" db:"notes"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
	Available      *int       `json:"available,omitempty" db:"-" doc:"Bungalows of the type free for the nights now"`
}

const waitlistSelect = `SELECT w.id, w.waitlist_no, w.customer_id, w.guest_name, w.guest_phone, w.guest_email, w.bungalow_type_id, t.name AS type_name,
	to_char(w.arrival_date, 'YYYY-MM-DD') AS arrival_date, w.nights, w.adults, w.children, w.rate_plan_code, w.priority, w.booking_source, w.status, w.offered_at,
	w.stay_id, s.stay_no, w.notes, w.created_at
	FROM stay.waitlist w JOIN stay.bungalow_types t ON t.id = w.bungalow_type_id LEFT JOIN stay.stays s ON s.id = w.stay_id`

const waitlistOrder = ` ORDER BY CASE w.priority WHEN 'vip' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, w.created_at`

// WaitlistInput adds a guest to the waitlist.
type WaitlistInput struct {
	CustomerID     *uuid.UUID  `json:"customerId,omitempty"`
	Guest          *GuestInput `json:"guest,omitempty"`
	BungalowTypeID uuid.UUID   `json:"bungalowTypeId"`
	ArrivalDate    string      `json:"arrivalDate" doc:"YYYY-MM-DD"`
	Nights         int         `json:"nights"`
	Adults         int         `json:"adults,omitempty"`
	Children       int         `json:"children,omitempty"`
	RatePlanCode   string      `json:"ratePlanCode,omitempty"`
	Priority       string      `json:"priority,omitempty" enum:"low,normal,high,vip"`
	BookingSource  string      `json:"bookingSource,omitempty" enum:"website,member_app,guest_app,front_desk,phone,walk_in,corporate"`
	Notes          string      `json:"notes,omitempty"`
}

func (m *Module) joinWaitlist(ctx context.Context, tx pgx.Tx, property uuid.UUID, in WaitlistInput) (WaitlistEntry, error) {
	if _, err := time.Parse(time.DateOnly, in.ArrivalDate); err != nil {
		return WaitlistEntry{}, handle.Invalid("arrivalDate", "invalid_date", "arrivalDate must be YYYY-MM-DD")
	}
	if in.Nights <= 0 {
		return WaitlistEntry{}, handle.Invalid("nights", "invalid", "at least one night")
	}
	if in.Adults <= 0 {
		in.Adults = 1
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	if in.BookingSource == "" {
		in.BookingSource = "front_desk"
	}
	if err := checkCapacity(ctx, tx, in.BungalowTypeID, in.Adults, in.Children); err != nil {
		return WaitlistEntry{}, err
	}
	name, phone, email := "", "", ""
	if in.Guest != nil {
		name, phone, email = in.Guest.Name, in.Guest.Phone, in.Guest.Email
	}
	if in.CustomerID != nil {
		c, err := crm.GetCustomer(ctx, tx, *in.CustomerID)
		if err != nil {
			return WaitlistEntry{}, err
		}
		name, phone, email = c.Name, nonEmpty(phone, c.Phone), nonEmpty(email, c.Email)
	} else if in.Guest != nil && (phone != "" || email != "") {
		c, _, err := crm.FindOrCreate(ctx, tx, property, crm.Identity{Name: name, Phone: phone, Email: email})
		if err != nil {
			return WaitlistEntry{}, err
		}
		in.CustomerID = &c.ID
	}
	if err := handle.Required("guest.name", name); err != nil {
		return WaitlistEntry{}, err
	}
	loc := calendar.Location(ctx, tx)
	no, err := numbering.Next(ctx, tx, property, "WL", clock.Now().In(loc))
	if err != nil {
		return WaitlistEntry{}, err
	}
	wid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO stay.waitlist (id, property_id, waitlist_no, customer_id, guest_name, guest_phone, guest_email, bungalow_type_id,
		arrival_date, nights, adults, children, rate_plan_code, priority, booking_source, notes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::date,$10,$11,$12,$13,$14,$15,$16,$17)`, wid, property, no, in.CustomerID, name, nzs(phone), nzs(email),
		in.BungalowTypeID, in.ArrivalDate, in.Nights, in.Adults, in.Children, nzs(in.RatePlanCode), in.Priority, in.BookingSource, nzs(in.Notes),
		actor(ctx)); err != nil {
		return WaitlistEntry{}, err
	}
	w, err := m.waitlistEntry(ctx, tx, wid)
	if err != nil {
		return w, err
	}
	return w, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionCreate, EntityType: "stay.waitlist", EntityID: wid.String(),
		EntityLabel: no + " · " + name, PropertyID: &property, After: w})
}

func (m *Module) waitlistEntry(ctx context.Context, q dbtx.Querier, wid uuid.UUID) (WaitlistEntry, error) {
	rows, err := q.Query(ctx, waitlistSelect+` WHERE w.id = $1`, wid)
	return handle.One[WaitlistEntry](rows, err, "waitlist entry")
}

// freeUnits counts the bungalows of a type free for every night from a
// local arrival date.
func (m *Module) freeUnits(ctx context.Context, q dbtx.Querier, property, typeID uuid.UUID, arrival string, nights int) (int, error) {
	loc := calendar.Location(ctx, q)
	pol, _, err := m.policy(ctx, q, property)
	if err != nil {
		return 0, err
	}
	a, err := time.ParseInLocation(time.DateOnly, arrival, loc)
	if err != nil {
		return 0, err
	}
	start, end := clockOn(a, pol.CheckInTime, loc), clockOn(a.AddDate(0, 0, nights), pol.CheckOutTime, loc)
	rows, err := q.Query(ctx, `SELECT resource_id FROM stay.bungalows WHERE type_id = $1 AND status = 'active' AND archived_at IS NULL AND resource_id IS NOT NULL`, typeID)
	if err != nil {
		return 0, err
	}
	res, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	free := 0
	for _, r := range res {
		busy, err := m.Res.BusyResources(ctx, q, []uuid.UUID{r}, start, end)
		if err != nil {
			return 0, err
		}
		if busy == 0 {
			free++
		}
	}
	return free, nil
}

// offerWaitlist offers the freed bungalows of a type to the waiting guests
// in priority order (one guest per free bungalow) and notifies them.
func (m *Module) offerWaitlist(ctx context.Context, tx pgx.Tx, property, typeID uuid.UUID) error {
	loc := calendar.Location(ctx, tx)
	today := clock.Now().In(loc).Format(time.DateOnly)
	list, err := handle.List[WaitlistEntry](tx.Query(ctx, waitlistSelect+` WHERE w.property_id = $1 AND w.bungalow_type_id = $2 AND w.status = 'waiting'
		AND w.arrival_date >= $3::date`+waitlistOrder, property, typeID, today))
	if err != nil {
		return err
	}
	offered := map[string]int{}
	for _, w := range list {
		key := w.ArrivalDate + "/" + itoa(w.Nights)
		free, err := m.freeUnits(ctx, tx, property, typeID, w.ArrivalDate, w.Nights)
		if err != nil {
			return err
		}
		if free-offered[key] <= 0 {
			continue
		}
		offered[key]++
		if _, err := tx.Exec(ctx, `UPDATE stay.waitlist SET status = 'offered', offered_at = now() WHERE id = $1`, w.ID); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "offer", EntityType: "stay.waitlist", EntityID: w.ID.String(),
			EntityLabel: w.WaitlistNo + " · " + w.GuestName, PropertyID: &property, ActorName: "system:waitlist",
			After: map[string]any{"status": "offered", "available": free}}); err != nil {
			return err
		}
		data := map[string]any{"name": w.GuestName, "waitlistNo": w.WaitlistNo, "type": w.TypeName, "arrival": w.ArrivalDate, "nights": w.Nights}
		if err := m.notifyContact(ctx, tx, property, w.CustomerID, deref(w.GuestEmail), w.GuestName, "stay.waitlist_available", data); err != nil {
			return err
		}
		if err := m.notifyStaff(ctx, tx, property, "stay.waitlist.manage", "stay.ops_waitlist", map[string]any{"title": "Waitlist " + w.WaitlistNo,
			"body": w.GuestName + " · " + w.TypeName + " " + w.ArrivalDate + " is available"}, "/accommodation/waitlist"); err != nil {
			return err
		}
	}
	return nil
}

// WaitlistConvertInput converts an entry into a reservation.
type WaitlistConvertInput struct {
	RatePlan string        `json:"ratePlan,omitempty"`
	UnitID   *uuid.UUID    `json:"unitId,omitempty"`
	Payment  *PaymentInput `json:"payment,omitempty"`
	Notes    string        `json:"notes,omitempty"`
}

func (m *Module) convertWaitlist(ctx context.Context, tx pgx.Tx, property, wid uuid.UUID, in WaitlistConvertInput, key string) (StayResult, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM stay.waitlist WHERE id = $1 FOR UPDATE`, wid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return StayResult{}, errs.NotFound("waitlist entry")
		}
		return StayResult{}, err
	}
	w, err := m.waitlistEntry(ctx, tx, wid)
	if err != nil {
		return StayResult{}, err
	}
	if w.Status != "waiting" && w.Status != "offered" {
		return StayResult{}, errs.Conflict("invalid_status", "the waitlist entry is "+w.Status)
	}
	a, _ := time.Parse(time.DateOnly, w.ArrivalDate)
	plan := in.RatePlan
	if plan == "" {
		plan = deref(w.RatePlanCode)
	}
	sin := StayInput{Kind: "bungalow", ArrivalDate: w.ArrivalDate, DepartureDate: a.AddDate(0, 0, w.Nights).Format(time.DateOnly), Adults: w.Adults,
		Children: w.Children, RatePlan: plan, CustomerID: w.CustomerID, BookingSource: w.BookingSource, Notes: in.Notes, WaitlistID: &wid,
		Payment: in.Payment, Channel: "back_office"}
	if in.UnitID != nil {
		sin.UnitID = in.UnitID
	} else {
		t := w.BungalowTypeID
		sin.BungalowTypeID = &t
	}
	if w.CustomerID == nil {
		sin.Guest = &GuestInput{Name: w.GuestName, Phone: deref(w.GuestPhone), Email: deref(w.GuestEmail)}
	}
	return m.Book(ctx, tx, property, sin, key)
}

func (m *Module) cancelWaitlist(ctx context.Context, tx pgx.Tx, wid uuid.UUID, reason string) (WaitlistEntry, error) {
	w, err := m.waitlistEntry(ctx, tx, wid)
	if err != nil {
		return w, err
	}
	if w.Status != "waiting" && w.Status != "offered" {
		return w, errs.Conflict("invalid_status", "the waitlist entry is "+w.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.waitlist SET status = 'cancelled', notes = coalesce($2, notes), updated_by = $3 WHERE id = $1`, wid, nzs(reason),
		actor(ctx)); err != nil {
		return w, err
	}
	after, err := m.waitlistEntry(ctx, tx, wid)
	if err != nil {
		return after, err
	}
	pid := handle.Property(ctx)
	return after, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "cancel", EntityType: "stay.waitlist", EntityID: wid.String(),
		EntityLabel: w.WaitlistNo, PropertyID: &pid, Before: w, After: after, Reason: reason})
}

// expireWaitlist expires the entries whose arrival has passed.
func expireWaitlist(ctx context.Context, tx pgx.Tx, day string) error {
	_, err := tx.Exec(ctx, `UPDATE stay.waitlist SET status = 'expired' WHERE status IN ('waiting', 'offered') AND arrival_date < $1::date`, day)
	return err
}
