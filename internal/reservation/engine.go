package reservation

// Reservation Engine (PRD P2 EP-01). Public interface used by every business
// line (sport club, classes, stay, meeting rooms, driving range):
//
//	Book        multi-line reservation, all lines allocated or none (FR-RSV-05)
//	Confirm / Cancel / Reschedule / CheckIn / NoShow / Complete (FR-RSV-04)
//	Availability, Calendar (FR-RSV-03, FR-RSV-10)
//
// Two allocation modes are enforced by the database (FR-RSV-02):
//
//	exclusive  reservation.allocations + EXCLUDE (P0, unchanged for P1 tee times)
//	capacity   reservation.capacity_slots: booked ≤ capacity (CHECK + row lock)

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/rules"
)

// Statuses (Naming Convention §31; Draft = hold).
const (
	StatusDraft     = "draft"
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
	StatusCheckedIn = "checked_in"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
	StatusNoShow    = "no_show"
	StatusExpired   = "expired"
)

// Channels (FR-RSV-09).
var Channels = []string{"member_app", "website", "back_office", "walk_in", "import", "ops"}

// ErrCapacityFull is returned when a capacity slot has no room left.
var ErrCapacityFull = errs.Conflict("capacity_full", "no places left for this time")

// Engine is the reservation engine.
type Engine struct {
	DB      *dbtx.DB
	Events  *outbox.Bus
	Billing *billing.Service
	Notify  notify.Sender
	// Price returns an indicative price for availability (wired by the
	// composition root to Commercial pricing); nil disables prices.
	Price func(ctx context.Context, q dbtx.Querier, property uuid.UUID, rt ResourceType, res Resource, start, end time.Time, segment string) (*string, error)
}

// ResourceType is a registry entry (FR-RSV-01).
type ResourceType struct {
	ID               uuid.UUID `json:"id" db:"id"`
	Code             string    `json:"code" db:"code"`
	Name             string    `json:"name" db:"name"`
	BusinessLine     string    `json:"businessLine" db:"business_line"`
	ReservationModel string    `json:"reservationModel" db:"reservation_model"`
	AllocationMode   string    `json:"allocationMode" db:"allocation_mode"`
	SlotMinutes      int       `json:"slotMinutes" db:"slot_minutes"`
	BufferBefore     int       `json:"bufferBeforeMinutes" db:"buffer_before_minutes"`
	BufferAfter      int       `json:"bufferAfterMinutes" db:"buffer_after_minutes"`
	HoldMinutes      int       `json:"holdMinutes" db:"hold_minutes"`
	OpenTime         string    `json:"openTime" db:"open_time"`
	CloseTime        string    `json:"closeTime" db:"close_time"`
	CheckInTime      *string   `json:"checkInTime" db:"check_in_time"`
	CheckOutTime     *string   `json:"checkOutTime" db:"check_out_time"`
	ServiceType      *string   `json:"serviceType" db:"service_type"`
	Status           string    `json:"status" db:"status"`
}

// Resource is a bookable resource.
type Resource struct {
	ID           uuid.UUID      `json:"id" db:"id"`
	PropertyID   uuid.UUID      `json:"propertyId" db:"property_id"`
	Code         string         `json:"code" db:"code"`
	Name         string         `json:"name" db:"name"`
	ResourceType string         `json:"resourceType" db:"resource_type"`
	VenueID      *uuid.UUID     `json:"venueId" db:"venue_id"`
	Capacity     *int           `json:"capacity" db:"capacity"`
	Status       string         `json:"status" db:"status"`
	Attributes   map[string]any `json:"attributes" db:"attributes"`
}

// PriceItem is the item reference used for pricing (attribute priceItem,
// e.g. FUTSAL_SYNTHETIC, else the resource code).
func (r Resource) PriceItem() string {
	if v, ok := r.Attributes["priceItem"].(string); ok && v != "" {
		return v
	}
	return r.Code
}

const typeSelect = `SELECT id, code, name, business_line, reservation_model, allocation_mode, slot_minutes, buffer_before_minutes,
	buffer_after_minutes, hold_minutes, to_char(open_time, 'HH24:MI') AS open_time, to_char(close_time, 'HH24:MI') AS close_time,
	to_char(check_in_time, 'HH24:MI') AS check_in_time, to_char(check_out_time, 'HH24:MI') AS check_out_time, service_type, status
	FROM reservation.resource_types`

// Type returns a resource type by code.
func (e *Engine) Type(ctx context.Context, q dbtx.Querier, code string) (ResourceType, error) {
	rows, err := q.Query(ctx, typeSelect+` WHERE code = $1`, code)
	return handle.One[ResourceType](rows, err, "resource type "+code)
}

const resourceSelect = `SELECT id, property_id, code, name, resource_type, venue_id, capacity, status, attributes FROM reservation.resources`

// Resource returns a resource.
func (e *Engine) Resource(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (Resource, error) {
	rows, err := q.Query(ctx, resourceSelect+` WHERE id = $1`, rid)
	return handle.One[Resource](rows, err, "resource")
}

// ResourceRequest creates or updates the reservation resource behind a
// business object (court, bungalow, meeting room, equipment, bay …).
type ResourceRequest struct {
	PropertyID   uuid.UUID
	Code, Name   string
	ResourceType string
	VenueID      *uuid.UUID
	Capacity     *int
	Status       string
	Attributes   map[string]any
}

// EnsureResource creates the resource (or updates it when id is given).
func (e *Engine) EnsureResource(ctx context.Context, tx pgx.Tx, existing *uuid.UUID, r ResourceRequest) (uuid.UUID, error) {
	if r.Status == "" {
		r.Status = "active"
	}
	if r.Attributes == nil {
		r.Attributes = map[string]any{}
	}
	attrs, _ := json.Marshal(r.Attributes)
	if existing != nil {
		_, err := tx.Exec(ctx, `UPDATE reservation.resources SET name = $2, venue_id = $3, capacity = $4, status = $5,
			attributes = attributes || $6::jsonb, updated_by = $7 WHERE id = $1`, *existing, r.Name, r.VenueID, r.Capacity, r.Status, attrs, actor(ctx))
		return *existing, err
	}
	rid := id.New()
	code := strings.ToUpper(r.Code)
	if _, err := tx.Exec(ctx, `INSERT INTO reservation.resources (id, property_id, code, name, resource_type, venue_id, capacity, status, attributes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, rid, r.PropertyID, code, r.Name, r.ResourceType, r.VenueID, r.Capacity, r.Status, attrs, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return uuid.Nil, errs.Validation("duplicate", "a bookable resource with code "+code+" already exists",
				errs.Field("code", "taken", "code already used by another bookable resource"))
		}
		return uuid.Nil, err
	}
	return rid, nil
}

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func actorName(ctx context.Context) string {
	if p := authz.From(ctx); p != nil {
		return p.Name
	}
	return "system"
}

// ── reservations ──────────────────────────────────────────────────────────

// LineRequest is one line of a reservation (FR-RSV-05).
type LineRequest struct {
	ResourceID  uuid.UUID      `json:"resourceId"`
	Start       time.Time      `json:"start"`
	End         time.Time      `json:"end"`
	Quantity    int            `json:"quantity,omitempty" doc:"Places (capacity resources) or units; default 1"`
	Description string         `json:"description,omitempty"`
	Attributes  map[string]any `json:"attributes,omitempty"`
	// Set by business lines (not accepted from generic HTTP input).
	SnapshotID   *uuid.UUID       `json:"-"`
	Amount       *decimal.Decimal `json:"-"`
	SlotCapacity *int             `json:"-"` // capacity of a slot created for this line (class sessions)
	SkipCapacity bool             `json:"-"` // capacity line already reserved by the slot owner
}

// BookRequest creates a reservation.
type BookRequest struct {
	Lines            []LineRequest
	Kind             string // booking | block | class
	BusinessLine     string
	CustomerID       *uuid.UUID
	GuestName        string
	GuestPhone       string
	GuestEmail       string
	CorporateName    string
	Channel          string
	SourceType       string
	SourceID         *uuid.UUID
	RecurringGroupID *uuid.UUID
	Hold             bool // Draft with hold expiry
	Confirm          bool // confirmed immediately (else pending)
	DepositRequired  decimal.Decimal
	DepositDueAt     *time.Time
	FolioID          *uuid.UUID
	PolicyRefs       []rules.PolicyRef
	Notes            string
	Attributes       map[string]any
}

// Line is a stored reservation line.
type Line struct {
	ID                uuid.UUID      `json:"id" db:"id"`
	LineNo            int            `json:"lineNo" db:"line_no"`
	ResourceID        uuid.UUID      `json:"resourceId" db:"resource_id"`
	ResourceCode      string         `json:"resourceCode" db:"resource_code"`
	ResourceName      string         `json:"resourceName" db:"resource_name"`
	ResourceType      string         `json:"resourceType" db:"resource_type"`
	AllocationMode    string         `json:"allocationMode" db:"allocation_mode"`
	Start             time.Time      `json:"start" db:"start_at"`
	End               time.Time      `json:"end" db:"end_at"`
	Quantity          int            `json:"quantity" db:"quantity"`
	AllocationID      *uuid.UUID     `json:"allocationId" db:"allocation_id"`
	Status            string         `json:"status" db:"status" enum:"held,confirmed,checked_in,completed,released,cancelled"`
	Description       *string        `json:"description" db:"description"`
	PricingSnapshotID *uuid.UUID     `json:"pricingSnapshotId" db:"pricing_snapshot_id"`
	Amount            *string        `json:"amount" db:"amount"`
	Attributes        map[string]any `json:"attributes" db:"attributes"`
}

// HistoryEntry is one modification (FR-RSV-09).
type HistoryEntry struct {
	Action     string         `json:"action" db:"action"`
	FromStatus *string        `json:"fromStatus" db:"from_status"`
	ToStatus   *string        `json:"toStatus" db:"to_status"`
	Channel    string         `json:"channel" db:"channel"`
	Reason     *string        `json:"reason" db:"reason"`
	ActorName  *string        `json:"actorName" db:"actor_name"`
	Details    map[string]any `json:"details" db:"details"`
	CreatedAt  time.Time      `json:"createdAt" db:"created_at"`
}

// Reservation is a reservation with lines.
type Reservation struct {
	ID               uuid.UUID      `json:"id" db:"id"`
	PropertyID       uuid.UUID      `json:"propertyId" db:"property_id"`
	Code             string         `json:"code" db:"code"`
	Kind             string         `json:"kind" db:"kind" enum:"booking,block,class"`
	BusinessLine     string         `json:"businessLine" db:"business_line"`
	Status           string         `json:"status" db:"status" enum:"draft,pending,confirmed,checked_in,completed,cancelled,no_show,expired"`
	CustomerID       *uuid.UUID     `json:"customerId" db:"customer_id"`
	CustomerName     *string        `json:"customerName" db:"customer_name"`
	GuestName        *string        `json:"guestName" db:"guest_name"`
	GuestPhone       *string        `json:"guestPhone" db:"guest_phone"`
	GuestEmail       *string        `json:"guestEmail" db:"guest_email"`
	CorporateName    *string        `json:"corporateName" db:"corporate_name"`
	Channel          string         `json:"channel" db:"channel"`
	SourceType       *string        `json:"sourceType" db:"source_type"`
	SourceID         *uuid.UUID     `json:"sourceId" db:"source_id"`
	RecurringGroupID *uuid.UUID     `json:"recurringGroupId" db:"recurring_group_id"`
	HoldExpiresAt    *time.Time     `json:"holdExpiresAt" db:"hold_expires_at"`
	DepositRequired  string         `json:"depositRequired" db:"deposit_required"`
	DepositDueAt     *time.Time     `json:"depositDueAt" db:"deposit_due_at"`
	FolioID          *uuid.UUID     `json:"folioId" db:"folio_id"`
	Notes            *string        `json:"notes" db:"notes"`
	Attributes       map[string]any `json:"attributes" db:"attributes"`
	ConfirmedAt      *time.Time     `json:"confirmedAt" db:"confirmed_at"`
	CheckedInAt      *time.Time     `json:"checkedInAt" db:"checked_in_at"`
	CompletedAt      *time.Time     `json:"completedAt" db:"completed_at"`
	CancelledAt      *time.Time     `json:"cancelledAt" db:"cancelled_at"`
	CancelReason     *string        `json:"cancelReason" db:"cancel_reason"`
	CancellationFee  *string        `json:"cancellationFee" db:"cancellation_fee"`
	CreatedAt        time.Time      `json:"createdAt" db:"created_at"`
	Start            *time.Time     `json:"start" db:"start_at" doc:"Earliest line start"`
	End              *time.Time     `json:"end" db:"end_at" doc:"Latest line end"`
	Lines            []Line         `json:"lines" db:"-"`
	History          []HistoryEntry `json:"history,omitempty" db:"-"`
}

const reservationSelect = `SELECT r.id, r.property_id, r.code, r.kind, r.business_line, r.status, r.customer_id, c.name AS customer_name,
	r.guest_name, r.guest_phone, r.guest_email, r.corporate_name, r.channel, r.source_type, r.source_id, r.recurring_group_id, r.hold_expires_at,
	trim_scale(r.deposit_required)::text AS deposit_required, r.deposit_due_at, r.folio_id, r.notes, r.attributes, r.confirmed_at, r.checked_in_at, r.completed_at,
	r.cancelled_at, r.cancel_reason, trim_scale(r.cancellation_fee)::text AS cancellation_fee, r.created_at,
	(SELECT min(lower(l.period)) FROM reservation.reservation_lines l WHERE l.reservation_id = r.id) AS start_at,
	(SELECT max(upper(l.period)) FROM reservation.reservation_lines l WHERE l.reservation_id = r.id) AS end_at
	FROM reservation.reservations r LEFT JOIN reporting.customer_directory c ON c.id = r.customer_id`

const lineSelect = `SELECT l.id, l.line_no, l.resource_id, rs.code AS resource_code, rs.name AS resource_name, l.resource_type, l.allocation_mode,
	lower(l.period) AS start_at, upper(l.period) AS end_at, l.quantity, l.allocation_id, l.status, l.description, l.pricing_snapshot_id,
	trim_scale(l.amount)::text AS amount, l.attributes FROM reservation.reservation_lines l JOIN reservation.resources rs ON rs.id = l.resource_id`

// Get returns a reservation with lines (and history when withHistory).
func (e *Engine) Get(ctx context.Context, q dbtx.Querier, rid uuid.UUID, withHistory bool) (Reservation, error) {
	rows, err := q.Query(ctx, reservationSelect+` WHERE r.id = $1`, rid)
	res, err := handle.One[Reservation](rows, err, "reservation")
	if err != nil {
		return res, err
	}
	if res.Lines, err = handle.List[Line](q.Query(ctx, lineSelect+` WHERE l.reservation_id = $1 ORDER BY l.line_no`, rid)); err != nil {
		return res, err
	}
	if withHistory {
		res.History, err = handle.List[HistoryEntry](q.Query(ctx, `SELECT action, from_status, to_status, channel, reason, actor_name, details, created_at
			FROM reservation.reservation_history WHERE reservation_id = $1 ORDER BY created_at, id`, rid))
	}
	return res, err
}

func (e *Engine) history(ctx context.Context, tx pgx.Tx, r Reservation, action, from, to, channel, reason string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	if channel == "" {
		channel = r.Channel
	}
	raw, _ := json.Marshal(details)
	_, err := tx.Exec(ctx, `INSERT INTO reservation.reservation_history (id, property_id, reservation_id, action, from_status, to_status, channel,
		reason, actor_id, actor_name, details) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		id.New(), r.PropertyID, r.ID, action, nzs(from), nzs(to), channel, nzs(reason), actor(ctx), actorName(ctx), raw)
	return err
}

func nzs(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Book creates a reservation; every line is allocated or none (the caller's
// transaction rolls back on error).
func (e *Engine) Book(ctx context.Context, tx pgx.Tx, property uuid.UUID, req BookRequest) (Reservation, error) {
	if len(req.Lines) == 0 {
		return Reservation{}, handle.Invalid("lines", "required", "at least one line is required")
	}
	if req.Channel == "" {
		req.Channel = "back_office"
	}
	if !slices.Contains(Channels, req.Channel) {
		return Reservation{}, handle.Invalid("channel", "invalid_channel", "unknown channel "+req.Channel)
	}
	if req.Kind == "" {
		req.Kind = "booking"
	}
	loc := calendar.Location(ctx, tx)
	now := clock.Now()
	status := StatusPending
	var holdUntil *time.Time
	holdMinutes := 0
	type prepared struct {
		LineRequest
		res Resource
		rt  ResourceType
	}
	var lines []prepared
	for i, l := range req.Lines {
		res, err := e.Resource(ctx, tx, l.ResourceID)
		if err != nil {
			return Reservation{}, errs.Validation("invalid_resource", fmt.Sprintf("line %d: resource not found", i+1))
		}
		if res.PropertyID != property {
			return Reservation{}, errs.Validation("invalid_resource", fmt.Sprintf("line %d: resource belongs to another property", i+1))
		}
		if res.Status != "active" {
			return Reservation{}, errs.Conflict("resource_inactive", res.Name+" is not active")
		}
		rt, err := e.Type(ctx, tx, res.ResourceType)
		if err != nil {
			return Reservation{}, err
		}
		if !l.End.After(l.Start) {
			return Reservation{}, errs.Validation("invalid_period", fmt.Sprintf("line %d: end must be after start", i+1))
		}
		if l.Quantity <= 0 {
			l.Quantity = 1
		}
		if req.Kind == "booking" && !l.End.After(now) && req.Channel != "import" {
			return Reservation{}, errs.Validation("in_the_past", fmt.Sprintf("line %d: the period has already ended", i+1))
		}
		if rt.HoldMinutes > holdMinutes {
			holdMinutes = rt.HoldMinutes
		}
		lines = append(lines, prepared{l, res, rt})
	}
	if req.Hold {
		status = StatusDraft
		t := now.Add(time.Duration(holdMinutes) * time.Minute)
		holdUntil = &t
	} else if req.Confirm {
		status = StatusConfirmed
	}
	code, err := numbering.Next(ctx, tx, property, "RSV", now.In(loc))
	if err != nil {
		return Reservation{}, err
	}
	rid := id.New()
	attrs := req.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	rawAttrs, _ := json.Marshal(attrs)
	refs := req.PolicyRefs
	if refs == nil {
		refs = []rules.PolicyRef{}
	}
	rawRefs, _ := json.Marshal(refs)
	line := req.BusinessLine
	if line == "" {
		line = lines[0].rt.BusinessLine
	}
	var confirmedAt *time.Time
	if status == StatusConfirmed {
		confirmedAt = &now
	}
	if _, err := tx.Exec(ctx, `INSERT INTO reservation.reservations (id, property_id, code, kind, business_line, status, customer_id, guest_name,
		guest_phone, guest_email, corporate_name, channel, source_type, source_id, recurring_group_id, hold_expires_at, deposit_required, deposit_due_at,
		folio_id, policy_refs, notes, attributes, confirmed_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::numeric,$18,$19,$20,$21,$22,$23,$24)`,
		rid, property, code, req.Kind, line, status, req.CustomerID, nzs(req.GuestName), nzs(req.GuestPhone), nzs(req.GuestEmail),
		nzs(req.CorporateName), req.Channel, nzs(req.SourceType), req.SourceID, req.RecurringGroupID, holdUntil, req.DepositRequired.String(),
		req.DepositDueAt, req.FolioID, rawRefs, nzs(req.Notes), rawAttrs, confirmedAt, actor(ctx)); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return Reservation{}, errs.Validation("invalid_customer", "customer not found")
		}
		return Reservation{}, err
	}
	lineStatus := "confirmed"
	if status == StatusDraft {
		lineStatus = "held"
	}
	for i, l := range lines {
		lid := id.New()
		la, _ := json.Marshal(nonNilMap(l.Attributes))
		var amt *string
		if l.Amount != nil {
			s := l.Amount.String()
			amt = &s
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reservation.reservation_lines (id, property_id, reservation_id, line_no, resource_id, resource_type,
			allocation_mode, period, quantity, status, description, pricing_snapshot_id, amount, attributes, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,tstzrange($8,$9,'[)'),$10,$11,$12,$13,$14::numeric,$15,$16)`,
			lid, property, rid, i+1, l.res.ID, l.rt.Code, l.rt.AllocationMode, l.Start, l.End, l.Quantity, lineStatus, nzs(l.Description),
			l.SnapshotID, amt, la, actor(ctx)); err != nil {
			return Reservation{}, err
		}
		if err := e.allocate(ctx, tx, property, rid, lid, l.res, l.rt, l.LineRequest, lineStatus == "held", holdUntil, i+1); err != nil {
			return Reservation{}, err
		}
	}
	r, err := e.Get(ctx, tx, rid, false)
	if err != nil {
		return r, err
	}
	if err := e.history(ctx, tx, r, "created", "", status, req.Channel, "", map[string]any{"lines": len(lines)}); err != nil {
		return r, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "reservation", Action: audit.ActionCreate, EntityType: "reservation.reservation",
		EntityID: rid.String(), EntityLabel: code, PropertyID: &property, After: r}); err != nil {
		return r, err
	}
	if status == StatusConfirmed && req.Kind != "block" {
		if err := e.publish(ctx, tx, "reservation.confirmed", r); err != nil {
			return r, err
		}
	}
	return r, nil
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func (e *Engine) publish(ctx context.Context, tx pgx.Tx, event string, r Reservation) error {
	_, err := e.Events.Publish(ctx, tx, event, "reservation.reservation", &r.ID, &r.PropertyID, map[string]any{
		"reservationId": r.ID, "code": r.Code, "status": r.Status, "businessLine": r.BusinessLine, "customerId": r.CustomerID,
		"sourceType": r.SourceType, "sourceId": r.SourceID, "start": r.Start, "end": r.End, "channel": r.Channel})
	return err
}

// allocate locks the line's resource for its period.
func (e *Engine) allocate(ctx context.Context, tx pgx.Tx, property, rid, lid uuid.UUID, res Resource, rt ResourceType, l LineRequest,
	held bool, holdUntil *time.Time, lineNo int) error {
	if rt.AllocationMode == "exclusive" {
		start := l.Start.Add(-time.Duration(rt.BufferBefore) * time.Minute)
		end := l.End.Add(time.Duration(rt.BufferAfter) * time.Minute)
		var a Allocation
		var err error
		if held {
			a, err = Hold(ctx, tx, property, res.ID, rid, start, end, holdUntil.Sub(clock.Now()))
		} else {
			a, err = Confirm(ctx, tx, property, res.ID, rid, start, end)
		}
		if err == ErrSlotTaken {
			return errs.Conflict("slot_taken", fmt.Sprintf("line %d: %s is already booked for an overlapping period", lineNo, res.Name))
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE reservation.reservation_lines SET allocation_id = $2 WHERE id = $1`, lid, a.ID)
		return err
	}
	if l.SkipCapacity {
		return nil
	}
	periods, err := e.units(ctx, tx, rt, l.Start, l.End)
	if err != nil {
		return err
	}
	for _, p := range periods {
		slot, err := e.slot(ctx, tx, property, res, p[0], p[1], l.SlotCapacity)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE reservation.capacity_slots SET booked = booked + $2 WHERE id = $1 AND status = 'open' AND booked + $2 <= capacity`,
			slot, l.Quantity)
		if err != nil {
			if ok, _ := dbtx.IsCheckViolation(err); ok {
				return ErrCapacityFull
			}
			return err
		}
		if tag.RowsAffected() == 0 {
			return errs.Conflict("capacity_full", fmt.Sprintf("line %d: %s has no places left for %s", lineNo, res.Name,
				p[0].In(calendar.Location(ctx, tx)).Format("Mon 2 Jan 15:04")))
		}
		status := "confirmed"
		var exp *time.Time
		if held {
			status, exp = "held", holdUntil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reservation.capacity_allocations (id, property_id, slot_id, resource_id, reservation_id, line_id, quantity,
			status, expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id.New(), property, slot, res.ID, rid, lid, l.Quantity, status, exp); err != nil {
			return err
		}
	}
	return nil
}

// units splits a capacity booking into slot periods: class sessions use the
// period as-is; other types use the slot length aligned to local midnight.
func (e *Engine) units(ctx context.Context, q dbtx.Querier, rt ResourceType, start, end time.Time) ([][2]time.Time, error) {
	if rt.ReservationModel == "class_session" {
		return [][2]time.Time{{start, end}}, nil
	}
	loc := calendar.Location(ctx, q)
	step := time.Duration(rt.SlotMinutes) * time.Minute
	ls := start.In(loc)
	midnight := time.Date(ls.Year(), ls.Month(), ls.Day(), 0, 0, 0, 0, loc)
	offset := ls.Sub(midnight)
	cur := midnight.Add(offset / step * step)
	var out [][2]time.Time
	for cur.Before(end) {
		next := cur.Add(step)
		if rt.SlotMinutes >= 1440 {
			d := cur.In(loc)
			next = time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc)
		}
		out = append(out, [2]time.Time{cur, next})
		cur = next
		if len(out) > 500 {
			return nil, errs.Validation("period_too_long", "the booking spans too many slots")
		}
	}
	return out, nil
}

// slot returns the capacity slot for a period, creating it on first use.
func (e *Engine) slot(ctx context.Context, tx pgx.Tx, property uuid.UUID, res Resource, start, end time.Time, capOverride *int) (uuid.UUID, error) {
	find := func() (uuid.UUID, error) {
		var sid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM reservation.capacity_slots WHERE resource_id = $1 AND period = tstzrange($2, $3, '[)')`,
			res.ID, start, end).Scan(&sid)
		return sid, err
	}
	sid, err := find()
	if err == nil {
		return sid, nil
	}
	if !dbtx.IsNoRows(err) {
		return uuid.Nil, err
	}
	capacity := capOverride
	if capacity == nil {
		capacity = res.Capacity
	}
	if capacity == nil {
		return uuid.Nil, errs.Conflict("capacity_not_configured", res.Name+" has no capacity configured")
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	sid = id.New()
	_, err = sp.Exec(ctx, `INSERT INTO reservation.capacity_slots (id, property_id, resource_id, period, capacity) VALUES ($1,$2,$3,tstzrange($4,$5,'[)'),$6)`,
		sid, property, res.ID, start, end, *capacity)
	if err != nil {
		_ = sp.Rollback(ctx)
		if dbtx.IsExclusionViolation(err) {
			// created concurrently with the same period, or a different grid
			if s, ferr := find(); ferr == nil {
				return s, nil
			}
			return uuid.Nil, errs.Conflict("slot_grid_mismatch", res.Name+" already has a capacity slot overlapping this period with different times")
		}
		return uuid.Nil, err
	}
	return sid, sp.Commit(ctx)
}

// CreateCapacitySlot opens a capacity slot explicitly (class session with
// its own capacity, FR-CLS-03).
func (e *Engine) CreateCapacitySlot(ctx context.Context, tx pgx.Tx, property, resourceID uuid.UUID, start, end time.Time, capacity int, sourceType string, sourceID uuid.UUID) (uuid.UUID, error) {
	sid := id.New()
	_, err := tx.Exec(ctx, `INSERT INTO reservation.capacity_slots (id, property_id, resource_id, period, capacity, source_type, source_id)
		VALUES ($1,$2,$3,tstzrange($4,$5,'[)'),$6,$7,$8)`, sid, property, resourceID, start, end, capacity, sourceType, sourceID)
	if dbtx.IsExclusionViolation(err) {
		return uuid.Nil, errs.Conflict("slot_overlap", "another session of this resource overlaps this time")
	}
	return sid, err
}

// CloseCapacitySlot closes a slot (session cancelled by the club).
func (e *Engine) CloseCapacitySlot(ctx context.Context, tx pgx.Tx, sourceType string, sourceID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE reservation.capacity_slots SET status = 'closed' WHERE source_type = $1 AND source_id = $2`, sourceType, sourceID)
	return err
}

// SlotUsage returns capacity and booked places of the slot owned by a source.
func (e *Engine) SlotUsage(ctx context.Context, q dbtx.Querier, sourceType string, sourceID uuid.UUID) (capacity, booked int, err error) {
	err = q.QueryRow(ctx, `SELECT capacity, booked FROM reservation.capacity_slots WHERE source_type = $1 AND source_id = $2`, sourceType, sourceID).Scan(&capacity, &booked)
	if dbtx.IsNoRows(err) {
		return 0, 0, nil
	}
	return
}

// release frees allocations of the given lines (all lines when nil).
func (e *Engine) release(ctx context.Context, tx pgx.Tx, rid uuid.UUID, lineIDs []uuid.UUID, allocStatus string) error {
	filter := ``
	args := []any{rid, allocStatus}
	if lineIDs != nil {
		filter = ` AND l.id = ANY($3)`
		args = append(args, lineIDs)
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.allocations a SET status = $2 FROM reservation.reservation_lines l
		WHERE l.reservation_id = $1 AND a.id = l.allocation_id AND a.status IN ('held', 'confirmed')`+filter, args...); err != nil {
		return err
	}
	// capacity: give the places back
	if _, err := tx.Exec(ctx, `WITH freed AS (
			UPDATE reservation.capacity_allocations ca SET status = $2 FROM reservation.reservation_lines l
			WHERE ca.line_id = l.id AND l.reservation_id = $1 AND ca.status IN ('held', 'confirmed')`+filter+`
			RETURNING ca.slot_id, ca.quantity)
		UPDATE reservation.capacity_slots s SET booked = s.booked - f.q
		FROM (SELECT slot_id, sum(quantity) AS q FROM freed GROUP BY slot_id) f WHERE s.id = f.slot_id`, args...); err != nil {
		return err
	}
	return nil
}

func (e *Engine) lock(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (Reservation, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM reservation.reservations WHERE id = $1 FOR UPDATE`, rid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return Reservation{}, errs.NotFound("reservation")
		}
		return Reservation{}, err
	}
	return e.Get(ctx, tx, rid, false)
}

func (e *Engine) setStatus(ctx context.Context, tx pgx.Tx, before Reservation, to, action, reason string, extra string, args ...any) (Reservation, error) {
	q := `UPDATE reservation.reservations SET status = $2, updated_by = $3` + extra + ` WHERE id = $1`
	if _, err := tx.Exec(ctx, q, append([]any{before.ID, to, actor(ctx)}, args...)...); err != nil {
		return before, err
	}
	after, err := e.Get(ctx, tx, before.ID, false)
	if err != nil {
		return after, err
	}
	if err := e.history(ctx, tx, after, action, before.Status, to, "", reason, nil); err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "reservation", Action: audit.ActionStatusChange, EntityType: "reservation.reservation",
		EntityID: before.ID.String(), EntityLabel: before.Code, PropertyID: &before.PropertyID, Before: before, After: after, Reason: reason})
}

// DepositPolicy is the per resource type deposit rule (FR-RSV-08).
type DepositPolicy struct {
	Percent           string `json:"percent"`
	Fixed             string `json:"fixed"`
	DueHours          int    `json:"dueHours"`
	RequiredToConfirm bool   `json:"requiredToConfirm"`
}

// CancellationPolicy is the per resource type cancellation rule (FR-RSV-07).
type CancellationPolicy struct {
	FreeCancelHours  int    `json:"freeCancelHours"`
	FeePercent       string `json:"feePercent"`
	NoShowFeePercent string `json:"noShowFeePercent"`
}

// Policies returns deposit and cancellation policy of a resource type.
func (e *Engine) Policies(ctx context.Context, q dbtx.Querier, property uuid.UUID, resourceType string) (DepositPolicy, rules.PolicyRef, CancellationPolicy, rules.PolicyRef, error) {
	dep, dref, err := rules.PolicyAt(ctx, q, "reservation.deposit."+resourceType, property, DepositPolicy{Percent: "0", Fixed: "0", DueHours: 24})
	if err != nil {
		return dep, dref, CancellationPolicy{}, rules.PolicyRef{}, err
	}
	can, cref, err := rules.PolicyAt(ctx, q, "reservation.cancellation."+resourceType, property,
		CancellationPolicy{FreeCancelHours: 24, FeePercent: "50", NoShowFeePercent: "100"})
	return dep, dref, can, cref, err
}

// DepositFor computes the required deposit for an amount.
func (p DepositPolicy) DepositFor(total decimal.Decimal) decimal.Decimal {
	pct, _ := decimal.NewFromString(p.Percent)
	fixed, _ := decimal.NewFromString(p.Fixed)
	d := total.Mul(pct).Div(decimal.NewFromInt(100)).Round(0).Add(fixed)
	if d.GreaterThan(total) {
		return total
	}
	return d
}

// Confirm confirms a draft or pending reservation.
func (e *Engine) Confirm(ctx context.Context, tx pgx.Tx, rid uuid.UUID, force bool) (Reservation, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status == StatusConfirmed {
		return r, nil
	}
	if r.Status != StatusDraft && r.Status != StatusPending {
		return r, errs.Conflict("invalid_status", "only Draft or Pending reservations can be confirmed (is "+r.Status+")")
	}
	if r.Status == StatusDraft && r.HoldExpiresAt != nil && !r.HoldExpiresAt.After(clock.Now()) {
		return r, errs.Conflict("hold_expired", "the hold has expired; create a new booking")
	}
	if dep, _ := decimal.NewFromString(r.DepositRequired); dep.IsPositive() && !force && r.FolioID != nil {
		dp, _, _, _, perr := e.Policies(ctx, tx, r.PropertyID, r.Lines[0].ResourceType)
		if perr != nil {
			return r, perr
		}
		if dp.RequiredToConfirm {
			f, err := billing.GetFolio(ctx, tx, *r.FolioID)
			if err != nil {
				return r, err
			}
			// P1 keeps a deposit apart from settlement payments until applied
			paid, _ := decimal.NewFromString(f.Summary.Payments)
			held, _ := decimal.NewFromString(f.Summary.HeldDeposits)
			if paid.Add(held).LessThan(dep) {
				return r, errs.Conflict("deposit_required", "a deposit of "+dep.StringFixed(0)+" is required before confirmation")
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.allocations a SET status = 'confirmed', expires_at = NULL FROM reservation.reservation_lines l
		WHERE l.reservation_id = $1 AND a.id = l.allocation_id AND a.status = 'held'`, rid); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.capacity_allocations SET status = 'confirmed', expires_at = NULL WHERE reservation_id = $1 AND status = 'held'`, rid); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'confirmed' WHERE reservation_id = $1 AND status = 'held'`, rid); err != nil {
		return r, err
	}
	after, err := e.setStatus(ctx, tx, r, StatusConfirmed, "confirmed", "", `, confirmed_at = now(), hold_expires_at = NULL`)
	if err != nil {
		return after, err
	}
	return after, e.publish(ctx, tx, "reservation.confirmed", after)
}

// CancelResult reports a cancellation.
type CancelResult struct {
	Reservation Reservation     `json:"reservation"`
	Fee         string          `json:"fee"`
	Refunded    string          `json:"refunded"`
	Policy      rules.PolicyRef `json:"policy"`
}

// Cancel cancels a reservation, applying the cancellation policy of the
// first line's resource type (fee waived when waive=true).
func (e *Engine) Cancel(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string, waive bool) (CancelResult, error) {
	return e.cancel(ctx, tx, rid, reason, waive, nil)
}

// CancelWithFee cancels a reservation with a fee computed by the business
// line (e.g. the cancellation policy of an accommodation rate plan): the
// folio charges are voided, the fee is charged and the rest refunded.
func (e *Engine) CancelWithFee(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string, fee decimal.Decimal) (CancelResult, error) {
	return e.cancel(ctx, tx, rid, reason, false, &fee)
}

func (e *Engine) cancel(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string, waive bool, override *decimal.Decimal) (CancelResult, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return CancelResult{}, err
	}
	if !slices.Contains([]string{StatusDraft, StatusPending, StatusConfirmed}, r.Status) {
		return CancelResult{Reservation: r}, errs.Conflict("invalid_status", "a "+r.Status+" reservation cannot be cancelled")
	}
	fee := decimal.Zero
	var cref rules.PolicyRef
	if override != nil {
		fee = *override
	} else if r.FolioID != nil && len(r.Lines) > 0 && !waive && r.Status != StatusDraft {
		_, _, cp, ref, err := e.Policies(ctx, tx, r.PropertyID, r.Lines[0].ResourceType)
		if err != nil {
			return CancelResult{}, err
		}
		cref = ref
		if r.Start != nil && r.Start.Sub(clock.Now()) < time.Duration(cp.FreeCancelHours)*time.Hour {
			f, err := billing.GetFolio(ctx, tx, *r.FolioID)
			if err != nil {
				return CancelResult{}, err
			}
			charges, _ := decimal.NewFromString(f.Summary.Charges)
			pct, _ := decimal.NewFromString(cp.FeePercent)
			fee = charges.Mul(pct).Div(decimal.NewFromInt(100)).Round(0)
		}
	}
	if err := e.release(ctx, tx, rid, nil, "cancelled"); err != nil {
		return CancelResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'cancelled' WHERE reservation_id = $1`, rid); err != nil {
		return CancelResult{}, err
	}
	refunded := decimal.Zero
	if r.FolioID != nil {
		if refunded, err = e.Billing.SettleCancellation(ctx, tx, *r.FolioID, fee, r.BusinessLine, reason); err != nil {
			return CancelResult{}, err
		}
	}
	after, err := e.setStatus(ctx, tx, r, StatusCancelled, "cancelled", reason, `, cancelled_at = now(), cancel_reason = $4, cancellation_fee = $5::numeric`,
		nzs(reason), fee.String())
	if err != nil {
		return CancelResult{}, err
	}
	if err := e.publish(ctx, tx, "reservation.cancelled", after); err != nil {
		return CancelResult{}, err
	}
	return CancelResult{Reservation: after, Fee: fee.String(), Refunded: refunded.String(), Policy: cref}, nil
}

// LineChange moves one line (reschedule, FR-RSV-07).
type LineChange struct {
	LineID     uuid.UUID  `json:"lineId"`
	ResourceID *uuid.UUID `json:"resourceId,omitempty" doc:"Move to another resource of the same type"`
	Start      time.Time  `json:"start"`
	End        time.Time  `json:"end"`
}

// Reschedule moves lines to new periods/resources atomically.
func (e *Engine) Reschedule(ctx context.Context, tx pgx.Tx, rid uuid.UUID, changes []LineChange, reason string) (Reservation, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if !slices.Contains([]string{StatusDraft, StatusPending, StatusConfirmed}, r.Status) {
		return r, errs.Conflict("invalid_status", "a "+r.Status+" reservation cannot be rescheduled")
	}
	if len(changes) == 0 {
		return r, handle.Invalid("lines", "required", "at least one line change is required")
	}
	details := []map[string]any{}
	for _, c := range changes {
		var old *Line
		for i := range r.Lines {
			if r.Lines[i].ID == c.LineID {
				old = &r.Lines[i]
			}
		}
		if old == nil {
			return r, errs.Validation("invalid_line", "line not found in this reservation")
		}
		if !c.End.After(c.Start) {
			return r, errs.Validation("invalid_period", "end must be after start")
		}
		resID := old.ResourceID
		if c.ResourceID != nil {
			resID = *c.ResourceID
		}
		res, err := e.Resource(ctx, tx, resID)
		if err != nil {
			return r, err
		}
		if res.ResourceType != old.ResourceType {
			return r, errs.Validation("invalid_resource", "a line can only move to a resource of the same type")
		}
		rt, err := e.Type(ctx, tx, res.ResourceType)
		if err != nil {
			return r, err
		}
		if err := e.release(ctx, tx, rid, []uuid.UUID{old.ID}, "released"); err != nil {
			return r, err
		}
		held := old.Status == "held"
		if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET resource_id = $2, period = tstzrange($3,$4,'[)'), allocation_id = NULL,
			updated_by = $5 WHERE id = $1`, old.ID, resID, c.Start, c.End, actor(ctx)); err != nil {
			return r, err
		}
		if err := e.allocate(ctx, tx, r.PropertyID, rid, old.ID, res, rt, LineRequest{ResourceID: resID, Start: c.Start, End: c.End, Quantity: old.Quantity},
			held, r.HoldExpiresAt, old.LineNo); err != nil {
			return r, err
		}
		details = append(details, map[string]any{"lineNo": old.LineNo, "from": map[string]any{"resource": old.ResourceCode, "start": old.Start, "end": old.End},
			"to": map[string]any{"resource": res.Code, "start": c.Start, "end": c.End}})
	}
	after, err := e.Get(ctx, tx, rid, false)
	if err != nil {
		return after, err
	}
	if err := e.history(ctx, tx, after, "rescheduled", r.Status, r.Status, "", reason, map[string]any{"changes": details}); err != nil {
		return after, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "reservation", Action: "reschedule", EntityType: "reservation.reservation",
		EntityID: rid.String(), EntityLabel: r.Code, PropertyID: &r.PropertyID, Before: r, After: after, Reason: reason}); err != nil {
		return after, err
	}
	return after, e.publish(ctx, tx, "reservation.rescheduled", after)
}

// CheckIn marks a reservation as Checked-in.
func (e *Engine) CheckIn(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (Reservation, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status == StatusCheckedIn {
		return r, nil
	}
	if r.Status != StatusConfirmed {
		return r, errs.Conflict("invalid_status", "only Confirmed reservations can be checked in (is "+r.Status+")")
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'checked_in' WHERE reservation_id = $1 AND status = 'confirmed'`, rid); err != nil {
		return r, err
	}
	after, err := e.setStatus(ctx, tx, r, StatusCheckedIn, "checked_in", "", `, checked_in_at = now()`)
	if err != nil {
		return after, err
	}
	return after, e.publish(ctx, tx, "reservation.checked_in", after)
}

// NoShow marks a confirmed reservation as No-show and frees the resource.
func (e *Engine) NoShow(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string) (Reservation, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status != StatusConfirmed && r.Status != StatusPending {
		return r, errs.Conflict("invalid_status", "only Confirmed or Pending reservations can be marked No-show")
	}
	if err := e.release(ctx, tx, rid, nil, "released"); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'released' WHERE reservation_id = $1`, rid); err != nil {
		return r, err
	}
	after, err := e.setStatus(ctx, tx, r, StatusNoShow, "no_show", reason, "")
	if err != nil {
		return after, err
	}
	return after, e.publish(ctx, tx, "reservation.no_show", after)
}

// Complete closes a reservation after service.
func (e *Engine) Complete(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (Reservation, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status == StatusCompleted {
		return r, nil
	}
	if r.Status != StatusCheckedIn && r.Status != StatusConfirmed {
		return r, errs.Conflict("invalid_status", "only Confirmed or Checked-in reservations can be completed")
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'completed' WHERE reservation_id = $1 AND status IN ('confirmed', 'checked_in')`, rid); err != nil {
		return r, err
	}
	after, err := e.setStatus(ctx, tx, r, StatusCompleted, "completed", "", `, completed_at = now()`)
	if err != nil {
		return after, err
	}
	return after, e.publish(ctx, tx, "reservation.completed", after)
}

// ReleaseLinesFrom frees capacity/exclusive allocations of lines from time t
// (early check-out, VIP suite actual end). Exclusive allocations are cut.
func (e *Engine) ShortenLine(ctx context.Context, tx pgx.Tx, lineID uuid.UUID, newEnd time.Time) error {
	var allocID *uuid.UUID
	var start time.Time
	var rtCode string
	if err := tx.QueryRow(ctx, `SELECT allocation_id, lower(period), resource_type FROM reservation.reservation_lines WHERE id = $1`, lineID).
		Scan(&allocID, &start, &rtCode); err != nil {
		return err
	}
	if !newEnd.After(start) {
		return nil
	}
	rt, err := e.Type(ctx, tx, rtCode)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET period = tstzrange(lower(period), $2, '[)') WHERE id = $1`, lineID, newEnd); err != nil {
		return err
	}
	if allocID != nil {
		_, err = tx.Exec(ctx, `UPDATE reservation.allocations SET period = tstzrange(lower(period), $2, '[)') WHERE id = $1`, *allocID,
			newEnd.Add(time.Duration(rt.BufferAfter)*time.Minute))
	}
	return err
}

// AdvanceLineStart moves the start of a line earlier (early check-in of a
// stay): the longer allocation is checked against other bookings by the
// EXCLUDE constraint.
func (e *Engine) AdvanceLineStart(ctx context.Context, tx pgx.Tx, lineID uuid.UUID, newStart time.Time) error {
	var allocID *uuid.UUID
	var start time.Time
	var rtCode, name string
	if err := tx.QueryRow(ctx, `SELECT l.allocation_id, lower(l.period), l.resource_type, rs.name FROM reservation.reservation_lines l
		JOIN reservation.resources rs ON rs.id = l.resource_id WHERE l.id = $1`, lineID).Scan(&allocID, &start, &rtCode, &name); err != nil {
		return err
	}
	if !newStart.Before(start) {
		return nil
	}
	rt, err := e.Type(ctx, tx, rtCode)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET period = tstzrange($2, upper(period), '[)') WHERE id = $1`, lineID, newStart); err != nil {
		return err
	}
	if allocID == nil {
		return nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	_, err = sp.Exec(ctx, `UPDATE reservation.allocations SET period = tstzrange($2, upper(period), '[)') WHERE id = $1`, *allocID,
		newStart.Add(-time.Duration(rt.BufferBefore)*time.Minute))
	if err != nil {
		_ = sp.Rollback(ctx)
		if dbtx.IsExclusionViolation(err) {
			return errs.Conflict("slot_taken", name+" is still occupied before the check-in time")
		}
		return err
	}
	return sp.Commit(ctx)
}

// ExtendLine lengthens a line (VIP Suite overtime, FR-VIP-03): the new
// allocation is checked against other bookings by the EXCLUDE constraint.
func (e *Engine) ExtendLine(ctx context.Context, tx pgx.Tx, lineID uuid.UUID, newEnd time.Time) error {
	var allocID *uuid.UUID
	var end time.Time
	var rtCode, name string
	if err := tx.QueryRow(ctx, `SELECT l.allocation_id, upper(l.period), l.resource_type, rs.name FROM reservation.reservation_lines l
		JOIN reservation.resources rs ON rs.id = l.resource_id WHERE l.id = $1`, lineID).Scan(&allocID, &end, &rtCode, &name); err != nil {
		return err
	}
	if !newEnd.After(end) {
		return handle.Invalid("end", "invalid_period", "the new end must be later than the current end")
	}
	rt, err := e.Type(ctx, tx, rtCode)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET period = tstzrange(lower(period), $2, '[)') WHERE id = $1`, lineID, newEnd); err != nil {
		return err
	}
	if allocID == nil {
		return nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	_, err = sp.Exec(ctx, `UPDATE reservation.allocations SET period = tstzrange(lower(period), $2, '[)') WHERE id = $1`, *allocID,
		newEnd.Add(time.Duration(rt.BufferAfter)*time.Minute))
	if err != nil {
		_ = sp.Rollback(ctx)
		if dbtx.IsExclusionViolation(err) {
			return errs.Conflict("slot_taken", name+" is booked by someone else in the extension period")
		}
		return err
	}
	return sp.Commit(ctx)
}

// SetFolio links the reservation folio.
func (e *Engine) SetFolio(ctx context.Context, tx pgx.Tx, rid, folioID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE reservation.reservations SET folio_id = $2 WHERE id = $1`, rid, folioID)
	return err
}

// SetDeposit records the deposit required by policy.
func (e *Engine) SetDeposit(ctx context.Context, tx pgx.Tx, rid uuid.UUID, amount decimal.Decimal, due *time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE reservation.reservations SET deposit_required = $2::numeric, deposit_due_at = $3 WHERE id = $1`, rid, amount.String(), due)
	return err
}

// SetLinePrice stores the price snapshot and amount on a line.
func (e *Engine) SetLinePrice(ctx context.Context, tx pgx.Tx, lineID uuid.UUID, snapshotID *uuid.UUID, amount decimal.Decimal) error {
	_, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET pricing_snapshot_id = $2, amount = $3::numeric WHERE id = $1`, lineID, snapshotID, amount.String())
	return err
}

// ExpireHolds releases Draft reservations whose hold expired (and capacity
// holds) — FR-RSV-04. Returns the number of expired reservations.
func ExpireHolds(ctx context.Context, db *dbtx.DB) (int64, error) {
	var n int64
	err := db.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		// the application clock decides, like the hold it set (Sport Club FR-133)
		now := clock.Now()
		if _, err := tx.Exec(ctx, `WITH freed AS (
				UPDATE reservation.capacity_allocations SET status = 'released' WHERE status = 'held' AND expires_at <= $1
				RETURNING slot_id, quantity)
			UPDATE reservation.capacity_slots s SET booked = s.booked - f.q
			FROM (SELECT slot_id, sum(quantity) AS q FROM freed GROUP BY slot_id) f WHERE s.id = f.slot_id`, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reservation.allocations SET status = 'released' WHERE status = 'held' AND expires_at <= $1`, now); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE reservation.reservations SET status = 'expired' WHERE status = 'draft' AND hold_expires_at <= $1
			RETURNING id, property_id, channel`, now)
		if err != nil {
			return err
		}
		type ex struct {
			id, prop uuid.UUID
			ch       string
		}
		var list []ex
		for rows.Next() {
			var x ex
			if err := rows.Scan(&x.id, &x.prop, &x.ch); err != nil {
				rows.Close()
				return err
			}
			list = append(list, x)
		}
		rows.Close()
		for _, x := range list {
			if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'released' WHERE reservation_id = $1`, x.id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO reservation.reservation_history (id, property_id, reservation_id, action, from_status, to_status, channel, actor_name)
				VALUES ($1,$2,$3,'hold_expired','draft','expired',$4,'system')`, id.New(), x.prop, x.id, x.ch); err != nil {
				return err
			}
		}
		n = int64(len(list))
		return nil
	})
	return n, err
}
