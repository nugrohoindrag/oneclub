package stay

// Guest requests (requirements §23): extra towel, extra bed, room cleaning,
// maintenance, laundry, transportation, food & beverage … from the front
// office, the phone or the guest's My Stay. A request goes Requested →
// Assigned → In Progress → Completed; room cleaning becomes a housekeeping
// task and maintenance a work order (completing them completes the
// request); a paid request is charged to the folio when it is completed.

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// Request types.
var RequestTypes = []string{"extra_towel", "extra_bed", "room_cleaning", "maintenance", "laundry", "transportation", "food_beverage", "other"}

// GuestRequest is a request of an in-house (or arriving) guest.
type GuestRequest struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	RequestNo          string     `json:"requestNo" db:"request_no"`
	StayID             uuid.UUID  `json:"stayId" db:"stay_id"`
	StayNo             string     `json:"stayNo" db:"stay_no"`
	Guest              *string    `json:"guest" db:"guest"`
	BungalowID         *uuid.UUID `json:"bungalowId" db:"bungalow_id"`
	BungalowCode       *string    `json:"bungalowCode" db:"bungalow_code"`
	RequestType        string     `json:"requestType" db:"request_type" enum:"extra_towel,extra_bed,room_cleaning,maintenance,laundry,transportation,food_beverage,other"`
	Description        *string    `json:"description" db:"description"`
	Quantity           int        `json:"quantity" db:"quantity"`
	AddonID            *uuid.UUID `json:"addonId" db:"addon_id"`
	AddonName          *string    `json:"addonName" db:"addon_name"`
	ChargeAmount       *string    `json:"chargeAmount" db:"charge_amount"`
	Status             string     `json:"status" db:"status" enum:"requested,assigned,in_progress,completed,cancelled"`
	AssignedTo         *string    `json:"assignedTo" db:"assigned_to"`
	Source             string     `json:"source" db:"source" enum:"front_desk,phone,member_app,guest_app"`
	ScheduledFor       *time.Time `json:"scheduledFor" db:"scheduled_for"`
	RequestedAt        time.Time  `json:"requestedAt" db:"created_at"`
	AssignedAt         *time.Time `json:"assignedAt" db:"assigned_at"`
	StartedAt          *time.Time `json:"startedAt" db:"started_at"`
	CompletedAt        *time.Time `json:"completedAt" db:"completed_at"`
	FolioLineID        *uuid.UUID `json:"folioLineId" db:"folio_line_id"`
	HousekeepingTaskID *uuid.UUID `json:"housekeepingTaskId" db:"housekeeping_task_id"`
	WorkOrderID        *uuid.UUID `json:"workOrderId" db:"work_order_id"`
	Notes              *string    `json:"notes" db:"notes"`
	MinutesToComplete  *int       `json:"minutesToComplete" db:"minutes_to_complete"`
}

const requestSelect = `SELECT g.id, g.request_no, g.stay_id, s.stay_no, coalesce(c.name, s.guest_name) AS guest, g.bungalow_id, b.code AS bungalow_code, g.request_type,
	g.description, g.quantity, g.addon_id, a.name AS addon_name, trim_scale(g.charge_amount)::text AS charge_amount, g.status, g.assigned_to, g.source,
	g.scheduled_for, g.created_at, g.assigned_at, g.started_at, g.completed_at, g.folio_line_id, g.housekeeping_task_id, g.work_order_id, g.notes,
	CASE WHEN g.completed_at IS NOT NULL THEN (extract(epoch FROM g.completed_at - g.created_at) / 60)::int END AS minutes_to_complete
	FROM stay.guest_requests g JOIN stay.stays s ON s.id = g.stay_id LEFT JOIN reporting.customer_directory c ON c.id = s.customer_id
	LEFT JOIN stay.bungalows b ON b.id = g.bungalow_id LEFT JOIN stay.addons a ON a.id = g.addon_id`

func (m *Module) guestRequest(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (GuestRequest, error) {
	rows, err := q.Query(ctx, requestSelect+` WHERE g.id = $1`, rid)
	return handle.One[GuestRequest](rows, err, "guest request")
}

// GuestRequestInput is a new request.
type GuestRequestInput struct {
	StayID       uuid.UUID  `json:"stayId"`
	RequestType  string     `json:"requestType" enum:"extra_towel,extra_bed,room_cleaning,maintenance,laundry,transportation,food_beverage,other"`
	Description  string     `json:"description,omitempty"`
	Quantity     int        `json:"quantity,omitempty"`
	AddonID      *uuid.UUID `json:"addonId,omitempty" doc:"Paid service: the add-on priced on the folio when completed"`
	ChargeAmount string     `json:"chargeAmount,omitempty" doc:"Paid service without an add-on: the amount charged when completed"`
	AssignedTo   string     `json:"assignedTo,omitempty"`
	ScheduledFor *time.Time `json:"scheduledFor,omitempty"`
	Source       string     `json:"source,omitempty" enum:"front_desk,phone,member_app,guest_app"`
	Notes        string     `json:"notes,omitempty"`
}

func (m *Module) createRequest(ctx context.Context, tx pgx.Tx, in GuestRequestInput) (GuestRequest, error) {
	if !slices.Contains(RequestTypes, in.RequestType) {
		return GuestRequest{}, handle.Invalid("requestType", "invalid", "unknown request type")
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	if in.Source == "" {
		in.Source = "front_desk"
	}
	s, err := m.Get(ctx, tx, in.StayID)
	if err != nil {
		return GuestRequest{}, err
	}
	if s.Status != "checked_in" && s.Status != "reserved" {
		return GuestRequest{}, errs.Conflict("invalid_status", "requests are taken for reserved and in-house stays")
	}
	var charge *string
	if in.ChargeAmount != "" {
		c, err := handle.Decimal("chargeAmount", in.ChargeAmount, decimal.Zero)
		if err != nil {
			return GuestRequest{}, err
		}
		cs := c.String()
		charge = &cs
	}
	if in.AddonID != nil {
		a, err := loadAddon(ctx, tx, s.PropertyID, *in.AddonID)
		if err != nil {
			return GuestRequest{}, err
		}
		if a.Availability == "booking" {
			return GuestRequest{}, handle.Invalid("addonId", "not_in_stay", a.Name+" is only sold with a booking")
		}
	}
	loc := calendar.Location(ctx, tx)
	no, err := numbering.Next(ctx, tx, s.PropertyID, "REQ", clock.Now().In(loc))
	if err != nil {
		return GuestRequest{}, err
	}
	status := "requested"
	var assignedAt *time.Time
	if in.AssignedTo != "" {
		status = "assigned"
		now := clock.Now()
		assignedAt = &now
	}
	var unit *uuid.UUID
	if s.Kind == "bungalow" {
		unit = &s.UnitID
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO stay.guest_requests (id, property_id, request_no, stay_id, bungalow_id, request_type, description, quantity, addon_id,
		charge_amount, status, assigned_to, source, scheduled_for, assigned_at, notes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11,$12,$13,$14,$15,$16,$17)`, rid, s.PropertyID, no, s.ID, unit, in.RequestType, nzs(in.Description),
		in.Quantity, in.AddonID, charge, status, nzs(in.AssignedTo), in.Source, in.ScheduledFor, assignedAt, nzs(in.Notes), actor(ctx)); err != nil {
		return GuestRequest{}, err
	}
	// room cleaning → housekeeping, maintenance → work order
	switch {
	case in.RequestType == "room_cleaning" && unit != nil:
		t, err := m.createHKTask(ctx, tx, s.PropertyID, hkTaskInput{BungalowID: *unit, StayID: &s.ID, TaskType: "stayover_cleaning", Priority: "high",
			AssignedTo: in.AssignedTo, Notes: no + " " + in.Description, Source: "guest_request", GuestRequestID: &rid})
		if err != nil {
			return GuestRequest{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE stay.guest_requests SET housekeeping_task_id = $2 WHERE id = $1`, rid, t.ID); err != nil {
			return GuestRequest{}, err
		}
	case in.RequestType == "maintenance":
		w, err := m.createWorkOrder(ctx, tx, s.PropertyID, WorkOrderInput{BungalowID: unit, Category: "other", Title: nonEmpty(in.Description, "Guest request "+no),
			Priority: "high", AssignedTo: in.AssignedTo, ReportedBy: "Guest " + s.StayNo}, "guest_request", map[string]*uuid.UUID{"request": &rid})
		if err != nil {
			return GuestRequest{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE stay.guest_requests SET work_order_id = $2 WHERE id = $1`, rid, w.ID); err != nil {
			return GuestRequest{}, err
		}
	}
	g, err := m.guestRequest(ctx, tx, rid)
	if err != nil {
		return g, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionCreate, EntityType: "stay.guest_request", EntityID: rid.String(),
		EntityLabel: no + " · " + s.StayNo, PropertyID: &s.PropertyID, After: g}); err != nil {
		return g, err
	}
	where := s.StayNo
	if g.BungalowCode != nil {
		where = *g.BungalowCode
	}
	return g, m.notifyStaff(ctx, tx, s.PropertyID, "stay.guest_request.work", "stay.ops_guest_request", map[string]any{"title": "Guest request " + where,
		"body": label(in.RequestType) + " × " + itoa(in.Quantity) + " " + in.Description, "requestNo": no}, "/ops/stay-desk/requests")
}

func (m *Module) lockRequest(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (GuestRequest, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM stay.guest_requests WHERE id = $1 FOR UPDATE`, rid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return GuestRequest{}, errs.NotFound("guest request")
		}
		return GuestRequest{}, err
	}
	return m.guestRequest(ctx, tx, rid)
}

// RequestMoveInput moves a request (assign, start, complete, cancel).
type RequestMoveInput struct {
	AssignedTo string `json:"assignedTo,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

func (m *Module) moveRequest(ctx context.Context, tx pgx.Tx, rid uuid.UUID, op string, in RequestMoveInput) (GuestRequest, error) {
	g, err := m.lockRequest(ctx, tx, rid)
	if err != nil {
		return g, err
	}
	open := g.Status == "requested" || g.Status == "assigned" || g.Status == "in_progress"
	if !open {
		return g, errs.Conflict("invalid_status", "the request is "+g.Status)
	}
	var set string
	var args []any
	switch op {
	case "assign":
		if err := handle.Required("assignedTo", in.AssignedTo); err != nil {
			return g, err
		}
		st := g.Status
		if st == "requested" {
			st = "assigned"
		}
		set, args = `, status = $3, assigned_to = $4, assigned_at = coalesce(assigned_at, now())`, []any{st, in.AssignedTo}
	case "start":
		set, args = `, status = 'in_progress', started_at = now(), assigned_to = coalesce(assigned_to, $3)`, []any{actorName(ctx, tx)}
	case "complete":
		if err := m.chargeRequest(ctx, tx, g); err != nil {
			return g, err
		}
		set = `, status = 'completed', completed_at = now(), started_at = coalesce(started_at, now())`
	case "cancel":
		set = `, status = 'cancelled'`
	default:
		return g, handle.Invalid("op", "invalid", "unknown operation")
	}
	if in.Notes != "" {
		set += `, notes = $` + itoa(3+len(args))
		args = append(args, in.Notes)
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.guest_requests SET updated_by = $2`+set+` WHERE id = $1`, append([]any{rid, actor(ctx)}, args...)...); err != nil {
		return g, err
	}
	after, err := m.guestRequest(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	pid := handle.Property(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: op, EntityType: "stay.guest_request", EntityID: rid.String(), EntityLabel: g.RequestNo,
		PropertyID: &pid, Before: g, After: after}); err != nil {
		return after, err
	}
	s, err := m.Get(ctx, tx, g.StayID)
	if err != nil {
		return after, err
	}
	return after, m.notifyGuestData(ctx, tx, s, "stay.guest_request_update", map[string]any{"requestNo": g.RequestNo, "request": label(g.RequestType),
		"status": label(after.Status)})
}

// chargeRequest posts a paid request to the folio (once).
func (m *Module) chargeRequest(ctx context.Context, tx pgx.Tx, g GuestRequest) error {
	if g.FolioLineID != nil {
		return nil
	}
	s, err := m.Get(ctx, tx, g.StayID)
	if err != nil || s.FolioID == nil {
		return err
	}
	var lid uuid.UUID
	switch {
	case g.AddonID != nil:
		a, err := loadAddon(ctx, tx, s.PropertyID, *g.AddonID)
		if err != nil {
			return err
		}
		l, err := priceAddon(ctx, tx, s.PropertyID, a, g.Quantity, 1, max(s.Adults, 1)+s.Children, decimal.Zero, clock.Now())
		if err != nil {
			return err
		}
		if !decOf(l.Total).IsPositive() {
			return nil
		}
		if lid, err = m.chargeAddon(ctx, tx, *s.FolioID, s.ReservationID, l); err != nil {
			return err
		}
		if err := m.insertAddon(ctx, tx, s.PropertyID, s.ID, pendingAddon{AddonLine: l, Source: "guest_request", FolioLineID: &lid}); err != nil {
			return err
		}
	case g.ChargeAmount != nil && decOf(*g.ChargeAmount).IsPositive():
		component := "other"
		if g.RequestType == "food_beverage" {
			component = "fnb"
		}
		if lid, err = m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ReferenceType: "stay.guest_request",
			ReferenceID: &g.ID, Description: label(g.RequestType) + " · " + g.RequestNo, Quantity: decimal.NewFromInt(int64(g.Quantity)),
			Net: decOf(*g.ChargeAmount)}, BusinessLine: billing.LineStay, RevenueComponent: component}); err != nil {
			return err
		}
	default:
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE stay.guest_requests SET folio_line_id = $2 WHERE id = $1`, g.ID, lid)
	return err
}

// completeRequestFromTask completes the request of a housekeeping task or
// work order once the work is done.
func (m *Module) completeRequestFromTask(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	g, err := m.guestRequest(ctx, tx, rid)
	if err != nil || g.Status == "completed" || g.Status == "cancelled" {
		return err
	}
	_, err = m.moveRequest(ctx, tx, rid, "complete", RequestMoveInput{})
	return err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
