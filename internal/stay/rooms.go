package stay

// Bungalow inventory operations (requirements §6, §7, §9, §10, §16): the
// housekeeping status of a unit apart from its reservation status, room
// blocks (Blocked / Maintenance / Out of Order) held as "block"
// reservations in the Reservation Engine so a blocked unit is never offered,
// the room status board, the room rack and the availability search with
// prices, and the units offered when a stay is assigned.

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/commercial"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/membership"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/reservation"
)

// hkNext lists the housekeeping statuses a unit may move to by hand.
var hkNext = map[string][]string{
	"dirty":     {"cleaning", "cleaned", "ready"},
	"cleaning":  {"dirty", "cleaned", "ready"},
	"cleaned":   {"dirty", "cleaning", "inspected", "ready"},
	"inspected": {"dirty", "cleaning", "ready"},
	"ready":     {"dirty", "cleaning"},
}

// setHKStatus moves the housekeeping status of a bungalow; readiness (P2)
// follows it: only a Ready bungalow is ready for check-in.
func (m *Module) setHKStatus(ctx context.Context, tx pgx.Tx, unitID uuid.UUID, status, reason string) error {
	var before string
	var pid uuid.UUID
	var code string
	if err := tx.QueryRow(ctx, `SELECT hk_status, property_id, code FROM stay.bungalows WHERE id = $1 FOR UPDATE`, unitID).Scan(&before, &pid, &code); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("bungalow")
		}
		return err
	}
	if before == status {
		return nil
	}
	readiness := "not_ready"
	if status == "ready" {
		readiness = "ready"
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.bungalows SET hk_status = $2, readiness = $3, hk_updated_at = now(), updated_by = $4 WHERE id = $1`,
		unitID, status, readiness, actor(ctx)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionStatusChange, EntityType: "stay.bungalow", EntityID: unitID.String(),
		EntityLabel: code, PropertyID: &pid, Before: map[string]any{"hkStatus": before}, After: map[string]any{"hkStatus": status}, Reason: reason}); err != nil {
		return err
	}
	_, err := m.Events.Publish(ctx, tx, "stay.room_status_changed", "stay.bungalow", &unitID, &pid, map[string]any{"bungalowId": unitID, "code": code,
		"from": before, "to": status})
	return err
}

// RoomStatusInput sets the housekeeping status by hand.
type RoomStatusInput struct {
	Status string `json:"status" enum:"dirty,cleaning,cleaned,inspected,ready"`
	Reason string `json:"reason,omitempty"`
}

func (m *Module) changeRoomStatus(ctx context.Context, tx pgx.Tx, unitID uuid.UUID, in RoomStatusInput) error {
	if !slices.Contains(HKStatuses, in.Status) {
		return handle.Invalid("status", "invalid", "status must be dirty, cleaning, cleaned, inspected or ready")
	}
	var cur string
	if err := tx.QueryRow(ctx, `SELECT hk_status FROM stay.bungalows WHERE id = $1`, unitID).Scan(&cur); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("bungalow")
		}
		return err
	}
	if cur == in.Status {
		// confirmed as it is (e.g. a room check): recorded all the same
		pid := handle.Property(ctx)
		return audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "confirm_status", EntityType: "stay.bungalow", EntityID: unitID.String(),
			PropertyID: &pid, After: map[string]any{"hkStatus": cur}, Reason: in.Reason})
	}
	if !slices.Contains(hkNext[cur], in.Status) {
		return errs.Conflict("invalid_transition", fmt.Sprintf("a %s bungalow cannot become %s", cur, in.Status))
	}
	return m.setHKStatus(ctx, tx, unitID, in.Status, in.Reason)
}

// ── room blocks ───────────────────────────────────────────────────────────

// RoomBlock is a Blocked / Maintenance / Out of Order period of a bungalow.
type RoomBlock struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	BlockNo       string     `json:"blockNo" db:"block_no"`
	BungalowID    uuid.UUID  `json:"bungalowId" db:"bungalow_id"`
	BungalowCode  string     `json:"bungalowCode" db:"bungalow_code"`
	BungalowName  string     `json:"bungalowName" db:"bungalow_name"`
	Kind          string     `json:"kind" db:"kind" enum:"blocked,maintenance,out_of_order"`
	Start         time.Time  `json:"start" db:"start_at"`
	End           time.Time  `json:"end" db:"end_at"`
	Reason        string     `json:"reason" db:"reason"`
	WorkOrderID   *uuid.UUID `json:"workOrderId" db:"work_order_id"`
	ReservationID *uuid.UUID `json:"reservationId" db:"reservation_id"`
	Status        string     `json:"status" db:"status" enum:"active,released"`
	ReleasedAt    *time.Time `json:"releasedAt" db:"released_at"`
	CreatedAt     time.Time  `json:"createdAt" db:"created_at"`
}

const blockSelect = `SELECT k.id, k.block_no, k.bungalow_id, b.code AS bungalow_code, b.name AS bungalow_name, k.kind, k.start_at, k.end_at, k.reason,
	k.work_order_id, k.reservation_id, k.status, k.released_at, k.created_at FROM stay.room_blocks k JOIN stay.bungalows b ON b.id = k.bungalow_id`

// BlockInput blocks a bungalow for a period.
type BlockInput struct {
	BungalowID uuid.UUID  `json:"bungalowId"`
	Kind       string     `json:"kind" enum:"blocked,maintenance,out_of_order"`
	StartDate  string     `json:"startDate,omitempty" doc:"YYYY-MM-DD (from the check-in time); or start"`
	EndDate    string     `json:"endDate,omitempty" doc:"YYYY-MM-DD (until the check-out time); or end"`
	Start      *time.Time `json:"start,omitempty"`
	End        *time.Time `json:"end,omitempty"`
	Reason     string     `json:"reason"`
}

// createBlock takes a bungalow off sale for a period (§6): a confirmed
// "block" reservation in the Reservation Engine; a stay in the period
// refuses the block.
func (m *Module) createBlock(ctx context.Context, tx pgx.Tx, property uuid.UUID, in BlockInput, workOrder *uuid.UUID) (RoomBlock, error) {
	if in.Kind == "" {
		in.Kind = "blocked"
	}
	if !slices.Contains([]string{"blocked", "maintenance", "out_of_order"}, in.Kind) {
		return RoomBlock{}, handle.Invalid("kind", "invalid", "kind must be blocked, maintenance or out_of_order")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return RoomBlock{}, err
	}
	loc := calendar.Location(ctx, tx)
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return RoomBlock{}, err
	}
	var start, end time.Time
	switch {
	case in.Start != nil:
		start = *in.Start
	case in.StartDate != "":
		d, err := time.ParseInLocation(time.DateOnly, in.StartDate, loc)
		if err != nil {
			return RoomBlock{}, handle.Invalid("startDate", "invalid_date", "startDate must be YYYY-MM-DD")
		}
		start = clockOn(d, pol.CheckOutTime, loc)
		if now := clock.Now(); start.Before(now) {
			start = now
		}
	default:
		start = clock.Now()
	}
	switch {
	case in.End != nil:
		end = *in.End
	case in.EndDate != "":
		d, err := time.ParseInLocation(time.DateOnly, in.EndDate, loc)
		if err != nil {
			return RoomBlock{}, handle.Invalid("endDate", "invalid_date", "endDate must be YYYY-MM-DD")
		}
		end = clockOn(d, pol.CheckInTime, loc)
	default:
		end = start.Add(24 * time.Hour)
	}
	if !end.After(start) {
		return RoomBlock{}, handle.Invalid("endDate", "invalid_period", "the block must end after it starts")
	}
	u, err := m.unit(ctx, tx, "bungalow", in.BungalowID)
	if err != nil {
		return RoomBlock{}, err
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return RoomBlock{}, err
	}
	res, err := m.Res.Book(ctx, sp, property, reservation.BookRequest{Kind: "block", BusinessLine: "stay", Channel: "back_office", Confirm: true,
		SourceType: "stay.room_block", Notes: in.Kind + ": " + in.Reason, Lines: []reservation.LineRequest{{ResourceID: *u.ResourceID, Start: start, End: end,
			Description: u.Name + " " + in.Kind}}})
	if err != nil {
		_ = sp.Rollback(ctx)
		if de, ok := errs.As(err); ok && de.Code == "slot_taken" {
			return RoomBlock{}, errs.Conflict("unit_booked", u.Name+" has a stay in this period; move the stay first")
		}
		return RoomBlock{}, err
	}
	if err := sp.Commit(ctx); err != nil {
		return RoomBlock{}, err
	}
	no, err := numbering.Next(ctx, tx, property, "BLK", clock.Now().In(loc))
	if err != nil {
		return RoomBlock{}, err
	}
	bid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO stay.room_blocks (id, property_id, block_no, bungalow_id, kind, start_at, end_at, reason, reservation_id, work_order_id,
		created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, bid, property, no, in.BungalowID, in.Kind, start, end, in.Reason, res.ID, workOrder,
		actor(ctx)); err != nil {
		return RoomBlock{}, err
	}
	if err := m.Res.SetSource(ctx, tx, res.ID, bid); err != nil {
		return RoomBlock{}, err
	}
	out, err := m.block(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionCreate, EntityType: "stay.room_block", EntityID: bid.String(),
		EntityLabel: no + " · " + u.Name + " · " + in.Kind, PropertyID: &property, After: out, Reason: in.Reason})
}

func (m *Module) block(ctx context.Context, q dbtx.Querier, bid uuid.UUID) (RoomBlock, error) {
	rows, err := q.Query(ctx, blockSelect+` WHERE k.id = $1`, bid)
	return handle.One[RoomBlock](rows, err, "room block")
}

// releaseBlock puts the bungalow back on sale.
func (m *Module) releaseBlock(ctx context.Context, tx pgx.Tx, bid uuid.UUID, reason string) (RoomBlock, error) {
	b, err := m.block(ctx, tx, bid)
	if err != nil {
		return b, err
	}
	if b.Status != "active" {
		return b, nil
	}
	if b.ReservationID != nil {
		if _, err := m.Res.Cancel(ctx, tx, *b.ReservationID, nonEmpty(reason, "released"), true); err != nil && !errs.Is(err, errs.KindConflict) {
			return b, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.room_blocks SET status = 'released', released_at = now(), released_by = $2, updated_by = $2 WHERE id = $1`,
		bid, actor(ctx)); err != nil {
		return b, err
	}
	out, err := m.block(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "release", EntityType: "stay.room_block", EntityID: bid.String(), EntityLabel: b.BlockNo,
		PropertyID: propertyOf(ctx), Before: b, After: out, Reason: reason}); err != nil {
		return out, err
	}
	var typeID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT type_id FROM stay.bungalows WHERE id = $1`, b.BungalowID).Scan(&typeID); err == nil {
		var pid uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT property_id FROM stay.room_blocks WHERE id = $1`, bid).Scan(&pid)
		return out, m.offerWaitlist(ctx, tx, pid, typeID)
	}
	return out, nil
}

// ── room status board (§10, §15) ─────────────────────────────────────────

// RoomState is a bungalow with its housekeeping, operational and
// reservation status.
type RoomState struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Code              string     `json:"code" db:"code"`
	Name              string     `json:"name" db:"name"`
	TypeID            uuid.UUID  `json:"typeId" db:"type_id"`
	TypeName          string     `json:"typeName" db:"type_name"`
	Location          *string    `json:"location" db:"location"`
	View              *string    `json:"view" db:"view"`
	Capacity          int        `json:"capacity" db:"capacity"`
	Status            string     `json:"status" db:"status"`
	HKStatus          string     `json:"hkStatus" db:"hk_status" enum:"dirty,cleaning,cleaned,inspected,ready"`
	HKUpdatedAt       *time.Time `json:"hkUpdatedAt" db:"hk_updated_at"`
	BlockKind         *string    `json:"blockKind" db:"block_kind"`
	BlockReason       *string    `json:"blockReason" db:"block_reason"`
	BlockUntil        *time.Time `json:"blockUntil" db:"block_until"`
	OperationalStatus string     `json:"operationalStatus" db:"operational_status" enum:"ready,dirty,cleaning,cleaned,inspected,maintenance,out_of_order,blocked"`
	ReservationStatus string     `json:"reservationStatus" db:"reservation_status" enum:"available,reserved,occupied,due_out,checked_out"`
	CurrentStayID     *uuid.UUID `json:"currentStayId" db:"current_stay_id"`
	CurrentStayNo     *string    `json:"currentStayNo" db:"current_stay_no"`
	CurrentGuest      *string    `json:"currentGuest" db:"current_guest"`
	CurrentDeparture  *time.Time `json:"currentDeparture" db:"current_departure"`
	ArrivalStayID     *uuid.UUID `json:"arrivalStayId" db:"arrival_stay_id"`
	ArrivalStayNo     *string    `json:"arrivalStayNo" db:"arrival_stay_no"`
	ArrivalGuest      *string    `json:"arrivalGuest" db:"arrival_guest"`
	ArrivalAt         *time.Time `json:"arrivalAt" db:"arrival_at"`
	OpenWorkOrders    int        `json:"openWorkOrders" db:"open_work_orders"`
	OpenHKTasks       int        `json:"openHkTasks" db:"open_hk_tasks"`
	Notes             *string    `json:"notes" db:"notes"`
}

// roomStateSQL lists the bungalows with their status at $2 for the local
// day [$2, $3).
const roomStateSQL = `WITH st AS (
	SELECT b.id, b.code, b.name, b.type_id, t.name AS type_name, b.location, b.view, coalesce(b.capacity, t.max_adults + t.max_children) AS capacity, b.status,
	  b.hk_status, b.hk_updated_at, b.notes, t.sort_order,
	  blk.kind AS block_kind, blk.reason AS block_reason, blk.end_at AS block_until,
	  cur.id AS current_stay_id, cur.stay_no AS current_stay_no, coalesce(cc.name, cur.guest_name) AS current_guest, cur.end_at AS current_departure,
	  arr.id AS arrival_stay_id, arr.stay_no AS arrival_stay_no, coalesce(ac.name, arr.guest_name) AS arrival_guest, arr.start_at AS arrival_at,
	  (SELECT count(*) FROM stay.work_orders w WHERE w.bungalow_id = b.id AND w.status IN ('open', 'assigned', 'in_progress'))::int AS open_work_orders,
	  (SELECT count(*) FROM stay.housekeeping_tasks h WHERE h.bungalow_id = b.id AND h.status IN ('open', 'in_progress', 'completed'))::int AS open_hk_tasks,
	  EXISTS (SELECT 1 FROM stay.stays x WHERE x.unit_id = b.id AND x.kind = 'bungalow' AND x.status = 'checked_out' AND x.checked_out_at >= $2
	    AND x.checked_out_at < $3) AS checked_out_today
	FROM stay.bungalows b JOIN stay.bungalow_types t ON t.id = b.type_id
	LEFT JOIN LATERAL (SELECT kind, reason, end_at FROM stay.room_blocks k WHERE k.bungalow_id = b.id AND k.status = 'active' AND k.start_at <= greatest($2, now())
	  AND k.end_at > greatest($2, now()) ORDER BY CASE kind WHEN 'out_of_order' THEN 0 WHEN 'maintenance' THEN 1 ELSE 2 END LIMIT 1) blk ON true
	LEFT JOIN LATERAL (SELECT s.* FROM stay.stays s WHERE s.unit_id = b.id AND s.kind = 'bungalow' AND s.status = 'checked_in' ORDER BY s.start_at LIMIT 1) cur ON true
	LEFT JOIN reporting.customer_directory cc ON cc.id = cur.customer_id
	LEFT JOIN LATERAL (SELECT s.* FROM stay.stays s WHERE s.unit_id = b.id AND s.kind = 'bungalow' AND s.status IN ('reserved', 'requested')
	  AND s.start_at < $3 AND s.end_at > $2 ORDER BY s.start_at LIMIT 1) arr ON true
	LEFT JOIN reporting.customer_directory ac ON ac.id = arr.customer_id
	WHERE b.property_id = $1 AND b.archived_at IS NULL AND ($4::uuid IS NULL OR b.type_id = $4))
	SELECT id, code, name, type_id, type_name, location, view, capacity, status, hk_status, hk_updated_at, block_kind, block_reason, block_until,
	  coalesce(block_kind, hk_status) AS operational_status,
	  CASE WHEN current_stay_id IS NOT NULL AND current_departure < $3 THEN 'due_out' WHEN current_stay_id IS NOT NULL THEN 'occupied'
	    WHEN arrival_stay_id IS NOT NULL THEN 'reserved' WHEN checked_out_today THEN 'checked_out' ELSE 'available' END AS reservation_status,
	  current_stay_id, current_stay_no, current_guest, current_departure, arrival_stay_id, arrival_stay_no, arrival_guest, arrival_at,
	  open_work_orders, open_hk_tasks, notes
	FROM st ORDER BY sort_order, type_name, code`

// RoomStates returns the status of every bungalow on a local day.
func (m *Module) RoomStates(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time, typeID *uuid.UUID) ([]RoomState, error) {
	loc := calendar.Location(ctx, q)
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	return handle.List[RoomState](q.Query(ctx, roomStateSQL, property, from, from.AddDate(0, 0, 1), typeID))
}

// ── room rack (§7) ────────────────────────────────────────────────────────

// RackSegment is a stay or block on the room rack.
type RackSegment struct {
	Kind          string     `json:"kind" enum:"stay,block"`
	ID            uuid.UUID  `json:"id"`
	Label         string     `json:"label"`
	Start         time.Time  `json:"start"`
	End           time.Time  `json:"end"`
	Status        string     `json:"status"`
	Guest         *string    `json:"guest,omitempty"`
	StayNo        *string    `json:"stayNo,omitempty"`
	PaymentStatus *string    `json:"paymentStatus,omitempty"`
	VIP           bool       `json:"vip"`
	TypeID        *uuid.UUID `json:"-"`
}

// RackUnit is one row of the room rack.
type RackUnit struct {
	ID                uuid.UUID     `json:"id"`
	Code              string        `json:"code"`
	Name              string        `json:"name"`
	TypeID            uuid.UUID     `json:"typeId"`
	TypeName          string        `json:"typeName"`
	HKStatus          string        `json:"hkStatus"`
	OperationalStatus string        `json:"operationalStatus"`
	Segments          []RackSegment `json:"segments"`
}

// RoomRack is the reservation calendar of the bungalows.
type RoomRack struct {
	From     string        `json:"from"`
	Days     int           `json:"days"`
	Dates    []string      `json:"dates"`
	Units    []RackUnit    `json:"units"`
	Unplaced []RackSegment `json:"unplaced" doc:"Stays booked by type without a bungalow in the period"`
}

// Rack builds the room rack of days from a local date.
func (m *Module) Rack(ctx context.Context, q dbtx.Querier, property uuid.UUID, from time.Time, days int, typeID *uuid.UUID, status string) (RoomRack, error) {
	loc := calendar.Location(ctx, q)
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, days)
	out := RoomRack{From: start.Format(time.DateOnly), Days: days, Dates: []string{}, Units: []RackUnit{}, Unplaced: []RackSegment{}}
	for i := 0; i < days; i++ {
		out.Dates = append(out.Dates, start.AddDate(0, 0, i).Format(time.DateOnly))
	}
	states, err := m.RoomStates(ctx, q, property, clock.Now().In(loc), typeID)
	if err != nil {
		return out, err
	}
	idx := map[uuid.UUID]int{}
	for _, s := range states {
		idx[s.ID] = len(out.Units)
		out.Units = append(out.Units, RackUnit{ID: s.ID, Code: s.Code, Name: s.Name, TypeID: s.TypeID, TypeName: s.TypeName, HKStatus: s.HKStatus,
			OperationalStatus: s.OperationalStatus, Segments: []RackSegment{}})
	}
	rows, err := q.Query(ctx, `SELECT s.id, s.unit_id, s.unit_type_id, s.unit_assigned, s.stay_no, coalesce(c.name, s.guest_name, s.corporate_name, ''), s.start_at,
		coalesce(s.actual_end_at, s.end_at), s.status, s.vip FROM stay.stays s LEFT JOIN reporting.customer_directory c ON c.id = s.customer_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status IN ('requested', 'reserved', 'checked_in', 'checked_out')
		AND s.start_at < $3 AND coalesce(s.actual_end_at, s.end_at) > $2 AND ($4 = '' OR s.status = ANY(string_to_array($4, ','))) ORDER BY s.start_at`,
		property, start, end, status)
	if err != nil {
		return out, err
	}
	type stayRow struct {
		seg      RackSegment
		unit     uuid.UUID
		assigned bool
	}
	var list []stayRow
	for rows.Next() {
		var r stayRow
		var guest, no string
		var typ *uuid.UUID
		if err := rows.Scan(&r.seg.ID, &r.unit, &typ, &r.assigned, &no, &guest, &r.seg.Start, &r.seg.End, &r.seg.Status, &r.seg.VIP); err != nil {
			rows.Close()
			return out, err
		}
		r.seg.Kind, r.seg.Label, r.seg.Guest, r.seg.StayNo, r.seg.TypeID = "stay", guest, &guest, &no, typ
		list = append(list, r)
	}
	rows.Close()
	for _, r := range list {
		i, ok := idx[r.unit]
		if !ok {
			if typeID == nil || (r.seg.TypeID != nil && *r.seg.TypeID == *typeID) {
				out.Unplaced = append(out.Unplaced, r.seg)
			}
			continue
		}
		out.Units[i].Segments = append(out.Units[i].Segments, r.seg)
	}
	blocks, err := handle.List[RoomBlock](q.Query(ctx, blockSelect+` WHERE k.property_id = $1 AND k.status = 'active' AND k.start_at < $3 AND k.end_at > $2
		ORDER BY k.start_at`, property, start, end))
	if err != nil {
		return out, err
	}
	for _, b := range blocks {
		if i, ok := idx[b.BungalowID]; ok && (status == "" || slices.Contains([]string{"blocked", "maintenance", "out_of_order"}, status)) {
			out.Units[i].Segments = append(out.Units[i].Segments, RackSegment{Kind: "block", ID: b.ID, Label: b.Reason, Start: b.Start, End: b.End, Status: b.Kind})
		}
	}
	return out, nil
}

// ── availability search with prices (§5, §6) ─────────────────────────────

// SearchRate is the price of a room type under one rate plan or package.
type SearchRate struct {
	Code              string  `json:"code"`
	Name              string  `json:"name"`
	Kind              string  `json:"kind" enum:"rate_plan,package,commercial"`
	Total             string  `json:"total"`
	AveragePerNight   string  `json:"averagePerNight"`
	Discount          string  `json:"discount"`
	PromotionName     *string `json:"promotionName"`
	IncludesBreakfast bool    `json:"includesBreakfast"`
	NonRefundable     bool    `json:"nonRefundable"`
	FreeCancelHours   int     `json:"freeCancelHours"`
	PaymentPolicy     string  `json:"paymentPolicy"`
	Error             *string `json:"error,omitempty" doc:"Why the plan cannot be booked for this stay"`
}

// SearchType is a room type in the availability search.
type SearchType struct {
	TypeID      uuid.UUID    `json:"typeId"`
	Code        string       `json:"code"`
	Name        string       `json:"name"`
	Description *string      `json:"description"`
	MaxAdults   int          `json:"maxAdults"`
	MaxChildren int          `json:"maxChildren"`
	Facilities  []string     `json:"facilities"`
	Photos      []string     `json:"photos"`
	Units       int          `json:"units"`
	Available   int          `json:"available" doc:"Bungalows free for every night of the stay"`
	FreeUnits   []uuid.UUID  `json:"freeUnits"`
	FitsGuests  bool         `json:"fitsGuests"`
	Rates       []SearchRate `json:"rates"`
}

// SearchInput is an availability search.
type SearchInput struct {
	Arrival     time.Time
	Departure   time.Time
	Adults      int
	Children    int
	PromoCode   string
	CorporateID *uuid.UUID
	CustomerID  *uuid.UUID
	Source      string
	TypeID      *uuid.UUID
}

// Search returns the room types with their free bungalows and prices.
func (m *Module) Search(ctx context.Context, tx pgx.Tx, property uuid.UUID, in SearchInput) ([]SearchType, error) {
	loc := calendar.Location(ctx, tx)
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	nights := int(in.Departure.Sub(in.Arrival).Hours()/24 + 0.5)
	if nights < 1 {
		return nil, handle.Invalid("departure", "invalid_period", "the departure must be after the arrival")
	}
	start, end := clockOn(in.Arrival, pol.CheckInTime, loc), clockOn(in.Departure, pol.CheckOutTime, loc)
	segment := "guest"
	if in.CustomerID != nil {
		if s, err := memberSegment(ctx, tx, property, *in.CustomerID); err == nil && s != "" {
			segment = s
		}
	}
	rows, err := tx.Query(ctx, `SELECT t.id, t.code, t.name, t.description, t.max_adults, t.max_children, t.facilities, t.photos,
		coalesce(array_agg(b.id ORDER BY b.code) FILTER (WHERE b.id IS NOT NULL), '{}'), coalesce(array_agg(b.resource_id ORDER BY b.code) FILTER (WHERE b.id IS NOT NULL), '{}')
		FROM stay.bungalow_types t LEFT JOIN stay.bungalows b ON b.type_id = t.id AND b.status = 'active' AND b.archived_at IS NULL AND b.resource_id IS NOT NULL
		WHERE t.property_id = $1 AND t.status = 'active' AND t.archived_at IS NULL AND ($2::uuid IS NULL OR t.id = $2)
		GROUP BY t.id ORDER BY t.sort_order, t.name`, property, in.TypeID)
	if err != nil {
		return nil, err
	}
	type typeRow struct {
		st        SearchType
		units     []uuid.UUID
		resources []uuid.UUID
	}
	var types []typeRow
	for rows.Next() {
		var r typeRow
		if err := rows.Scan(&r.st.TypeID, &r.st.Code, &r.st.Name, &r.st.Description, &r.st.MaxAdults, &r.st.MaxChildren, &r.st.Facilities, &r.st.Photos,
			&r.units, &r.resources); err != nil {
			rows.Close()
			return nil, err
		}
		types = append(types, r)
	}
	rows.Close()
	plans, err := handle.List[struct {
		Code string `db:"code"`
	}](tx.Query(ctx, `SELECT code FROM stay.rate_plans WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY is_default DESC, sort_order, name`,
		property))
	if err != nil {
		return nil, err
	}
	pkgs, err := handle.List[struct {
		Code string `db:"code"`
	}](tx.Query(ctx, `SELECT code FROM stay.packages WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, property))
	if err != nil {
		return nil, err
	}
	out := []SearchType{}
	for _, t := range types {
		st := t.st
		st.Units, st.FreeUnits, st.Rates = len(t.units), []uuid.UUID{}, []SearchRate{}
		if st.Photos == nil {
			st.Photos = []string{}
		}
		if st.Facilities == nil {
			st.Facilities = []string{}
		}
		st.FitsGuests = max(in.Adults, 1) <= st.MaxAdults && in.Children <= st.MaxChildren
		for i, rid := range t.resources {
			busy, err := m.Res.BusyResources(ctx, tx, []uuid.UUID{rid}, start, end)
			if err != nil {
				return nil, err
			}
			if busy == 0 {
				st.FreeUnits = append(st.FreeUnits, t.units[i])
			}
		}
		st.Available = len(st.FreeUnits)
		req := roomRequest{TypeID: st.TypeID, Arrival: in.Arrival, Departure: in.Departure, Adults: max(in.Adults, 1), Children: in.Children, Segment: segment,
			CorporateID: in.CorporateID, Source: in.Source, PromoCode: in.PromoCode, BookedAt: clock.Now(), Explicit: true}
		add := func(code, kind string, q *RoomQuote, qerr error) {
			r := SearchRate{Code: code, Name: code, Kind: kind}
			if qerr != nil {
				msg := qerr.Error()
				if de, ok := errs.As(qerr); ok {
					msg = de.Message
				}
				r.Error = &msg
			} else if q != nil {
				r.Name, r.Total, r.Discount, r.PromotionName = q.RatePlanName, q.Total, q.Discount, q.PromotionName
				r.AveragePerNight = decOf(q.Total).Div(decimalN(len(q.Nights))).Round(0).String()
				r.IncludesBreakfast, r.NonRefundable, r.FreeCancelHours, r.PaymentPolicy = q.IncludesBreakfast, q.NonRefundable, q.FreeCancelHours, q.PaymentPolicy
				for _, inc := range q.Inclusions {
					if !inc.Included {
						r.Total = decOf(r.Total).Add(decOf(inc.Total)).String()
					}
				}
			} else {
				return
			}
			st.Rates = append(st.Rates, r)
		}
		for _, p := range plans {
			r := req
			r.RatePlan = p.Code
			sp, err := tx.Begin(ctx)
			if err != nil {
				return nil, err
			}
			q, qerr := m.quoteRoom(ctx, sp, property, r)
			_ = sp.Rollback(ctx)
			add(p.Code, "rate_plan", q, qerr)
		}
		for _, p := range pkgs {
			r := req
			r.PackageCode = p.Code
			sp, err := tx.Begin(ctx)
			if err != nil {
				return nil, err
			}
			q, qerr := m.quoteRoom(ctx, sp, property, r)
			_ = sp.Rollback(ctx)
			if qerr != nil {
				if de, ok := errs.As(qerr); ok && de.Code == "package_type" {
					continue // the package is not offered for this type
				}
			}
			add(p.Code, "package", q, qerr)
		}
		if len(plans) == 0 || len(st.Rates) == 0 || allFailed(st.Rates) {
			// Commercial pricing (P2): the indicative room price
			var item string
			_ = tx.QueryRow(ctx, `SELECT coalesce(price_item, code) FROM stay.bungalow_types WHERE id = $1`, st.TypeID).Scan(&item)
			sp, err := tx.Begin(ctx)
			if err != nil {
				return nil, err
			}
			e := end
			pr, perr := commercial.Pricer{}.Resolve(ctx, sp, property, commercial.PriceRequest{ServiceType: "bungalow", ItemRef: item, Segment: segment,
				RatePlan: "ROOM_ONLY", Start: start, End: &e, Channel: "back_office"})
			_ = sp.Rollback(ctx)
			if perr == nil {
				st.Rates = append(st.Rates, SearchRate{Code: "ROOM_ONLY", Name: "Room Only", Kind: "commercial", Total: pr.Tax.Total,
					AveragePerNight: decOf(pr.Tax.Total).Div(decimalN(nights)).Round(0).String(), Discount: pr.Discount, PaymentPolicy: "deposit",
					FreeCancelHours: 24})
			}
		}
		out = append(out, st)
	}
	return out, nil
}

func allFailed(rs []SearchRate) bool {
	for _, r := range rs {
		if r.Error == nil {
			return false
		}
	}
	return true
}

// ── units offered for an assignment (§16) ─────────────────────────────────

// UnitOption is a bungalow that could take a stay, with its warnings.
type UnitOption struct {
	ID                uuid.UUID `json:"id"`
	Code              string    `json:"code"`
	Name              string    `json:"name"`
	TypeID            uuid.UUID `json:"typeId"`
	TypeName          string    `json:"typeName"`
	SameType          bool      `json:"sameType"`
	Free              bool      `json:"free" doc:"No other stay or block in the period"`
	HKStatus          string    `json:"hkStatus"`
	OperationalStatus string    `json:"operationalStatus"`
	Capacity          int       `json:"capacity"`
	Warnings          []string  `json:"warnings" doc:"occupied, maintenance, out_of_order, blocked, not_ready, over_capacity"`
	Current           bool      `json:"current"`
}

// UnitOptions lists the bungalows for assigning a stay, best first.
func (m *Module) UnitOptions(ctx context.Context, tx pgx.Tx, sid uuid.UUID) ([]UnitOption, error) {
	s, err := m.Get(ctx, tx, sid)
	if err != nil {
		return nil, err
	}
	if s.Kind != "bungalow" {
		return nil, errs.Validation("not_bungalow", "units are assigned to bungalow stays")
	}
	states, err := m.RoomStates(ctx, tx, s.PropertyID, clock.Now(), nil)
	if err != nil {
		return nil, err
	}
	out := []UnitOption{}
	for _, st := range states {
		if st.Status != "active" {
			continue
		}
		o := UnitOption{ID: st.ID, Code: st.Code, Name: st.Name, TypeID: st.TypeID, TypeName: st.TypeName, HKStatus: st.HKStatus,
			OperationalStatus: st.OperationalStatus, Capacity: st.Capacity, Warnings: []string{}, Current: st.ID == s.UnitID}
		o.SameType = s.UnitTypeID == nil || *s.UnitTypeID == st.TypeID
		var rid *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT resource_id FROM stay.bungalows WHERE id = $1`, st.ID).Scan(&rid); err != nil {
			return nil, err
		}
		busy := 0
		if rid != nil {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM reservation.allocations a WHERE a.resource_id = $1 AND a.status IN ('held', 'confirmed')
				AND a.period && tstzrange($2, $3, '[)') AND a.reservation_id <> $4`, *rid, s.Start, s.End, s.ReservationID).Scan(&busy); err != nil {
				return nil, err
			}
		}
		o.Free = busy == 0
		if !o.Free {
			o.Warnings = append(o.Warnings, "occupied")
		}
		if st.BlockKind != nil {
			o.Warnings = append(o.Warnings, *st.BlockKind)
		}
		if st.HKStatus != "ready" {
			o.Warnings = append(o.Warnings, "not_ready")
		}
		if s.Adults+s.Children > st.Capacity {
			o.Warnings = append(o.Warnings, "over_capacity")
		}
		out = append(out, o)
	}
	slices.SortStableFunc(out, func(a, b UnitOption) int {
		score := func(o UnitOption) int {
			n := len(o.Warnings) * 2
			if !o.SameType {
				n++
			}
			return n
		}
		return score(a) - score(b)
	})
	return out, nil
}

func decimalN(n int) decimal.Decimal { return decimal.NewFromInt(int64(max(n, 1))) }

// memberSegment is the pricing segment of a customer ("" = guest).
func memberSegment(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (string, error) {
	return membership.Segment(ctx, q, property, customer, "")
}

// propertyOf is the property of the request.
func propertyOf(ctx context.Context) *uuid.UUID {
	p := handle.Property(ctx)
	if p == uuid.Nil {
		return nil
	}
	return &p
}
