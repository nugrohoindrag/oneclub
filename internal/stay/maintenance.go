package stay

// Maintenance (requirements §22): work orders go Open → Assigned → In
// Progress → Resolved → Closed. A work order that needs the bungalow closed
// puts it Out of Order (a room block, so it cannot be booked) until it is
// resolved; the bungalow then waits for an inspection before it is Ready.
// Preventive schedules (AC cleaning, water heater, electrical inspection …)
// raise their work orders when due — by the daily accommodation job or by
// hand.

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// WorkOrder is a maintenance work order.
type WorkOrder struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	WONo           string     `json:"woNo" db:"wo_no"`
	BungalowID     *uuid.UUID `json:"bungalowId" db:"bungalow_id"`
	BungalowCode   *string    `json:"bungalowCode" db:"bungalow_code"`
	BungalowName   *string    `json:"bungalowName" db:"bungalow_name"`
	Category       string     `json:"category" db:"category" enum:"ac,plumbing,electrical,water_heater,furniture,appliance,structure,pest,other"`
	Title          string     `json:"title" db:"title"`
	Description    *string    `json:"description" db:"description"`
	Priority       string     `json:"priority" db:"priority" enum:"low,normal,high,urgent"`
	Status         string     `json:"status" db:"status" enum:"open,assigned,in_progress,resolved,closed,cancelled"`
	Source         string     `json:"source" db:"source" enum:"manual,guest_request,inspection,housekeeping,preventive"`
	AssignedTo     *string    `json:"assignedTo" db:"assigned_to"`
	ReportedBy     *string    `json:"reportedBy" db:"reported_by"`
	ClosesUnit     bool       `json:"closesUnit" db:"closes_unit"`
	BlockID        *uuid.UUID `json:"blockId" db:"block_id"`
	ExpectedEndAt  *time.Time `json:"expectedEndAt" db:"expected_end_at"`
	ScheduleID     *uuid.UUID `json:"scheduleId" db:"schedule_id"`
	ScheduleName   *string    `json:"scheduleName" db:"schedule_name"`
	DueDate        *string    `json:"dueDate" db:"due_date"`
	GuestRequestID *uuid.UUID `json:"guestRequestId" db:"guest_request_id"`
	Cost           *string    `json:"cost" db:"cost"`
	Resolution     *string    `json:"resolution" db:"resolution"`
	AssignedAt     *time.Time `json:"assignedAt" db:"assigned_at"`
	StartedAt      *time.Time `json:"startedAt" db:"started_at"`
	ResolvedAt     *time.Time `json:"resolvedAt" db:"resolved_at"`
	ClosedAt       *time.Time `json:"closedAt" db:"closed_at"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
	HoursOpen      *string    `json:"hoursOpen" db:"hours_open" doc:"Hours from report to resolution (or now)"`
}

const woSelect = `SELECT w.id, w.wo_no, w.bungalow_id, b.code AS bungalow_code, b.name AS bungalow_name, w.category, w.title, w.description, w.priority, w.status,
	w.source, w.assigned_to, w.reported_by, w.closes_unit, w.block_id, w.expected_end_at, w.schedule_id, ps.name AS schedule_name,
	to_char(w.due_date, 'YYYY-MM-DD') AS due_date, w.guest_request_id, trim_scale(w.cost)::text AS cost, w.resolution, w.assigned_at, w.started_at,
	w.resolved_at, w.closed_at, w.created_at, round(extract(epoch FROM coalesce(w.resolved_at, now()) - w.created_at) / 3600, 1)::text AS hours_open
	FROM stay.work_orders w LEFT JOIN stay.bungalows b ON b.id = w.bungalow_id LEFT JOIN stay.preventive_schedules ps ON ps.id = w.schedule_id`

func (m *Module) workOrder(ctx context.Context, q dbtx.Querier, wid uuid.UUID) (WorkOrder, error) {
	rows, err := q.Query(ctx, woSelect+` WHERE w.id = $1`, wid)
	return handle.One[WorkOrder](rows, err, "work order")
}

// WorkOrderInput reports a maintenance issue.
type WorkOrderInput struct {
	BungalowID    *uuid.UUID `json:"bungalowId,omitempty"`
	Category      string     `json:"category" enum:"ac,plumbing,electrical,water_heater,furniture,appliance,structure,pest,other"`
	Title         string     `json:"title"`
	Description   string     `json:"description,omitempty"`
	Priority      string     `json:"priority,omitempty" enum:"low,normal,high,urgent"`
	AssignedTo    string     `json:"assignedTo,omitempty"`
	ReportedBy    string     `json:"reportedBy,omitempty"`
	ClosesUnit    bool       `json:"closesUnit,omitempty" doc:"The bungalow is Out of Order (not bookable) until the work order is resolved"`
	ExpectedEndAt *time.Time `json:"expectedEndAt,omitempty" doc:"Out of Order until; default tomorrow"`
	DueDate       string     `json:"dueDate,omitempty"`
}

func (m *Module) createWorkOrder(ctx context.Context, tx pgx.Tx, property uuid.UUID, in WorkOrderInput, source string, link map[string]*uuid.UUID) (WorkOrder, error) {
	if err := handle.Required("title", in.Title); err != nil {
		return WorkOrder{}, err
	}
	if in.Category == "" {
		in.Category = "other"
	}
	if !slices.Contains(maintenanceCategories, in.Category) {
		return WorkOrder{}, handle.Invalid("category", "invalid", "unknown category")
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	if !slices.Contains(HKPriorities, in.Priority) {
		return WorkOrder{}, handle.Invalid("priority", "invalid", "priority must be low, normal, high or urgent")
	}
	if in.ClosesUnit && in.BungalowID == nil {
		return WorkOrder{}, handle.Invalid("bungalowId", "required", "only a bungalow can be put Out of Order")
	}
	if in.ReportedBy == "" {
		in.ReportedBy = actorName(ctx, tx)
	}
	loc := calendar.Location(ctx, tx)
	no, err := numbering.Next(ctx, tx, property, "WO", clock.Now().In(loc))
	if err != nil {
		return WorkOrder{}, err
	}
	status := "open"
	var assignedAt *time.Time
	if in.AssignedTo != "" {
		status = "assigned"
		now := clock.Now()
		assignedAt = &now
	}
	var due *string
	if in.DueDate != "" {
		due = &in.DueDate
	}
	wid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO stay.work_orders (id, property_id, wo_no, bungalow_id, category, title, description, priority, status, source, assigned_to,
		reported_by, closes_unit, expected_end_at, due_date, schedule_id, guest_request_id, assigned_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::date,$16,$17,$18,$19)`, wid, property, no, in.BungalowID, in.Category, in.Title,
		nzs(in.Description), in.Priority, status, source, nzs(in.AssignedTo), nzs(in.ReportedBy), in.ClosesUnit, in.ExpectedEndAt, due, link["schedule"],
		link["request"], assignedAt, actor(ctx)); err != nil {
		return WorkOrder{}, err
	}
	if in.ClosesUnit {
		end := clock.Now().Add(24 * time.Hour)
		if in.ExpectedEndAt != nil {
			end = *in.ExpectedEndAt
		}
		b, err := m.createBlock(ctx, tx, property, BlockInput{BungalowID: *in.BungalowID, Kind: "out_of_order", End: &end, Reason: no + " · " + in.Title}, &wid)
		if err != nil {
			return WorkOrder{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE stay.work_orders SET block_id = $2, expected_end_at = $3 WHERE id = $1`, wid, b.ID, end); err != nil {
			return WorkOrder{}, err
		}
	}
	w, err := m.workOrder(ctx, tx, wid)
	if err != nil {
		return w, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionCreate, EntityType: "stay.work_order", EntityID: wid.String(),
		EntityLabel: no + " · " + in.Title, PropertyID: &property, After: w}); err != nil {
		return w, err
	}
	where := ""
	if w.BungalowCode != nil {
		where = *w.BungalowCode + " · "
	}
	return w, m.notifyStaff(ctx, tx, property, "stay.work_order.work", "stay.ops_work_order", map[string]any{"title": "Work order " + no,
		"body": where + in.Title + " (" + in.Priority + ")", "woNo": no}, "/ops/stay-maintenance")
}

func (m *Module) lockWO(ctx context.Context, tx pgx.Tx, wid uuid.UUID) (WorkOrder, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM stay.work_orders WHERE id = $1 FOR UPDATE`, wid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return WorkOrder{}, errs.NotFound("work order")
		}
		return WorkOrder{}, err
	}
	return m.workOrder(ctx, tx, wid)
}

func (m *Module) woUpdate(ctx context.Context, tx pgx.Tx, before WorkOrder, action, set string, args ...any) (WorkOrder, error) {
	if _, err := tx.Exec(ctx, `UPDATE stay.work_orders SET updated_by = $2`+set+` WHERE id = $1`, append([]any{before.ID, actor(ctx)}, args...)...); err != nil {
		return WorkOrder{}, err
	}
	after, err := m.workOrder(ctx, tx, before.ID)
	if err != nil {
		return after, err
	}
	pid := handle.Property(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: action, EntityType: "stay.work_order", EntityID: before.ID.String(),
		EntityLabel: before.WONo + " · " + before.Title, PropertyID: &pid, Before: before, After: after}); err != nil {
		return after, err
	}
	if after.GuestRequestID != nil && (action == "resolve" || action == "close") {
		if err := m.completeRequestFromTask(ctx, tx, *after.GuestRequestID); err != nil {
			return after, err
		}
	}
	return after, nil
}

// WOAssignInput assigns a work order.
type WOAssignInput struct {
	AssignedTo string `json:"assignedTo"`
	Priority   string `json:"priority,omitempty" enum:"low,normal,high,urgent"`
}

// WOResolveInput resolves a work order.
type WOResolveInput struct {
	Resolution string `json:"resolution"`
	Cost       string `json:"cost,omitempty"`
}

// WOStatusInput moves a work order (assign, start, resolve, close, cancel).
func (m *Module) moveWorkOrder(ctx context.Context, tx pgx.Tx, wid uuid.UUID, op string, assign WOAssignInput, resolve WOResolveInput, reason string) (WorkOrder, error) {
	w, err := m.lockWO(ctx, tx, wid)
	if err != nil {
		return w, err
	}
	open := w.Status == "open" || w.Status == "assigned" || w.Status == "in_progress"
	switch op {
	case "assign":
		if !open {
			return w, errs.Conflict("invalid_status", "the work order is "+w.Status)
		}
		if err := handle.Required("assignedTo", assign.AssignedTo); err != nil {
			return w, err
		}
		prio := w.Priority
		if assign.Priority != "" && slices.Contains(HKPriorities, assign.Priority) {
			prio = assign.Priority
		}
		st := w.Status
		if st == "open" {
			st = "assigned"
		}
		return m.woUpdate(ctx, tx, w, "assign", `, status = $3, assigned_to = $4, priority = $5, assigned_at = coalesce(assigned_at, now())`, st, assign.AssignedTo, prio)
	case "start":
		if w.Status != "open" && w.Status != "assigned" {
			return w, errs.Conflict("invalid_status", "only open or assigned work orders can be started")
		}
		return m.woUpdate(ctx, tx, w, "start", `, status = 'in_progress', started_at = now(), assigned_to = coalesce(assigned_to, $3)`, actorName(ctx, tx))
	case "resolve":
		if !open {
			return w, errs.Conflict("invalid_status", "the work order is "+w.Status)
		}
		if err := handle.Required("resolution", resolve.Resolution); err != nil {
			return w, err
		}
		var cost *string
		if resolve.Cost != "" {
			c, err := handle.Decimal("cost", resolve.Cost, decimal.Zero)
			if err != nil {
				return w, err
			}
			cs := c.String()
			cost = &cs
		}
		if err := m.reopenUnit(ctx, tx, w); err != nil {
			return w, err
		}
		return m.woUpdate(ctx, tx, w, "resolve", `, status = 'resolved', resolved_at = now(), started_at = coalesce(started_at, now()), resolution = $3,
			cost = $4::numeric`, resolve.Resolution, cost)
	case "close":
		if w.Status != "resolved" {
			return w, errs.Conflict("invalid_status", "resolve the work order before closing it")
		}
		return m.woUpdate(ctx, tx, w, "close", `, status = 'closed', closed_at = now()`)
	case "cancel":
		if !open {
			return w, errs.Conflict("invalid_status", "the work order is "+w.Status)
		}
		if err := m.reopenUnit(ctx, tx, w); err != nil {
			return w, err
		}
		return m.woUpdate(ctx, tx, w, "cancel", `, status = 'cancelled', resolution = coalesce($3, resolution)`, nzs(reason))
	}
	return w, handle.Invalid("op", "invalid", "unknown operation")
}

// reopenUnit ends the Out of Order block of a work order; the bungalow is
// inspected before it is Ready again.
func (m *Module) reopenUnit(ctx context.Context, tx pgx.Tx, w WorkOrder) error {
	if w.BlockID == nil {
		return nil
	}
	if _, err := m.releaseBlock(ctx, tx, *w.BlockID, w.WONo+" resolved"); err != nil {
		return err
	}
	if w.BungalowID == nil {
		return nil
	}
	if err := m.setHKStatus(ctx, tx, *w.BungalowID, "cleaned", w.WONo+" resolved"); err != nil {
		return err
	}
	_, err := m.createHKTask(ctx, tx, handle.Property(ctx), hkTaskInput{BungalowID: *w.BungalowID, TaskType: "inspection", Priority: "high",
		Notes: "After " + w.WONo + ": " + w.Title, Source: "inspection"})
	return err
}

// GeneratePreventive raises the work orders of the preventive schedules
// due by day (one per bungalow of the schedule) and moves their next due
// date. Returns the work orders created.
func (m *Module) GeneratePreventive(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) ([]WorkOrder, error) {
	rows, err := tx.Query(ctx, `SELECT id, name, category, bungalow_id, interval_days, to_char(next_due, 'YYYY-MM-DD'), closes_unit, duration_hours,
		coalesce(assigned_to, ''), coalesce(notes, '') FROM stay.preventive_schedules WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND next_due <= $2::date ORDER BY next_due FOR UPDATE`, property, day.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	type sched struct {
		id              uuid.UUID
		name, cat, due  string
		unit            *uuid.UUID
		every, hours    int
		closes          bool
		assigned, notes string
	}
	var list []sched
	for rows.Next() {
		var s sched
		if err := rows.Scan(&s.id, &s.name, &s.cat, &s.unit, &s.every, &s.due, &s.closes, &s.hours, &s.assigned, &s.notes); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, s)
	}
	rows.Close()
	out := []WorkOrder{}
	for _, s := range list {
		units := []uuid.UUID{}
		if s.unit != nil {
			units = append(units, *s.unit)
		} else {
			r, err := tx.Query(ctx, `SELECT id FROM stay.bungalows WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY code`, property)
			if err != nil {
				return out, err
			}
			if units, err = pgx.CollectRows(r, pgx.RowTo[uuid.UUID]); err != nil {
				return out, err
			}
		}
		sid := s.id
		for _, u := range units {
			unit := u
			// a unit that is occupied is serviced without closing it
			closes := s.closes
			if closes {
				if blk, err := m.activeBlock(ctx, tx, unit, clock.Now()); err != nil {
					return out, err
				} else if blk != "" {
					closes = false
				}
			}
			var end *time.Time
			if closes {
				e := clock.Now().Add(time.Duration(s.hours) * time.Hour)
				end = &e
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return out, err
			}
			w, err := m.createWorkOrder(ctx, sp, property, WorkOrderInput{BungalowID: &unit, Category: s.cat, Title: s.name, Description: s.notes,
				AssignedTo: s.assigned, ClosesUnit: closes, ExpectedEndAt: end, DueDate: s.due, ReportedBy: "Preventive schedule"}, "preventive",
				map[string]*uuid.UUID{"schedule": &sid})
			if err != nil && closes && errs.Is(err, errs.KindConflict) {
				// the bungalow is booked: plan the work without closing it
				_ = sp.Rollback(ctx)
				if sp, err = tx.Begin(ctx); err != nil {
					return out, err
				}
				w, err = m.createWorkOrder(ctx, sp, property, WorkOrderInput{BungalowID: &unit, Category: s.cat, Title: s.name, Description: s.notes,
					AssignedTo: s.assigned, DueDate: s.due, ReportedBy: "Preventive schedule"}, "preventive", map[string]*uuid.UUID{"schedule": &sid})
			}
			if err != nil {
				_ = sp.Rollback(ctx)
				return out, err
			}
			if err := sp.Commit(ctx); err != nil {
				return out, err
			}
			out = append(out, w)
		}
		if _, err := tx.Exec(ctx, `UPDATE stay.preventive_schedules SET next_due = next_due + interval_days * (1 + floor(($2::date - next_due) / interval_days))::int,
			last_generated = $2::date WHERE id = $1`, s.id, day.Format(time.DateOnly)); err != nil {
			return out, err
		}
	}
	return out, nil
}
