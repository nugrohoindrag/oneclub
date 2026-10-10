package reservation

// Line-level operations (docs/requirement-booking-sportclub-mgcc.md §13.3):
// a court booking holds several lines (court × hours) paid at once; each
// line is checked in, played, finished or marked No-show on its own, and the
// status of the booking summarises its lines. Lines can be added to a
// booking (overtime, more hours) and a booking keyed in by mistake can be
// voided by a supervisor — not a cancellation by the guest: nothing is
// refunded here.

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
)

func (e *Engine) pickLines(r Reservation, lineIDs []uuid.UUID, statuses ...string) ([]Line, error) {
	var out []Line
	for _, l := range r.Lines {
		if len(lineIDs) > 0 && !slices.Contains(lineIDs, l.ID) {
			continue
		}
		if len(statuses) > 0 && !slices.Contains(statuses, l.Status) {
			if len(lineIDs) > 0 {
				return nil, errs.Conflict("invalid_line_status", "line "+itoa(l.LineNo)+" is "+l.Status)
			}
			continue
		}
		out = append(out, l)
	}
	if len(lineIDs) > 0 && len(out) != len(lineIDs) {
		return nil, errs.Validation("invalid_line", "line not found in this booking")
	}
	return out, nil
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func ids(ls []Line) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.ID)
	}
	return out
}

// settle sets the booking status from its lines: in play while a line is
// checked in, completed when every line is over (completed, no-show) and at
// least one was played, no-show when no line was played.
func (e *Engine) settle(ctx context.Context, tx pgx.Tx, before Reservation, action, reason string) (Reservation, error) {
	r, err := e.Get(ctx, tx, before.ID, false)
	if err != nil {
		return r, err
	}
	open, played, noShow := 0, 0, 0
	for _, l := range r.Lines {
		switch l.Status {
		case "held", "confirmed", "checked_in":
			open++
		case "completed":
			played++
		case "no_show":
			noShow++
		}
	}
	to := r.Status
	extra := ""
	switch {
	case slices.ContainsFunc(r.Lines, func(l Line) bool { return l.Status == "checked_in" }):
		to = StatusCheckedIn
		if r.CheckedInAt == nil {
			extra = `, checked_in_at = now()`
		}
	case open == 0 && played > 0:
		to, extra = StatusCompleted, `, completed_at = now()`
	case open == 0 && noShow > 0:
		to = StatusNoShow
	}
	if to == r.Status {
		if err := e.history(ctx, tx, r, action, r.Status, r.Status, "", reason, nil); err != nil {
			return r, err
		}
		return r, nil
	}
	after, err := e.setStatus(ctx, tx, r, to, action, reason, extra)
	if err != nil {
		return after, err
	}
	ev := map[string]string{StatusCheckedIn: "reservation.checked_in", StatusCompleted: "reservation.completed", StatusNoShow: "reservation.no_show"}[to]
	if ev != "" {
		return after, e.publish(ctx, tx, ev, after)
	}
	return after, nil
}

// CheckInLines checks in lines of a confirmed booking (all confirmed lines
// when lineIDs is empty).
func (e *Engine) CheckInLines(ctx context.Context, tx pgx.Tx, rid uuid.UUID, lineIDs []uuid.UUID) (Reservation, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status != StatusConfirmed && r.Status != StatusCheckedIn {
		return r, errs.Conflict("invalid_status", "only Confirmed bookings can be checked in (is "+r.Status+")")
	}
	ls, err := e.pickLines(r, lineIDs, "confirmed")
	if err != nil {
		return r, err
	}
	if len(ls) == 0 {
		return r, errs.Conflict("nothing_to_check_in", "no line of this booking is waiting for check-in")
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'checked_in', attributes = attributes || jsonb_build_object('checkedInAt', now())
		WHERE id = ANY($1)`, ids(ls)); err != nil {
		return r, err
	}
	return e.settle(ctx, tx, r, "checked_in", "")
}

// CompleteLines finishes lines in play (or confirmed lines whose time is
// over); all such lines when lineIDs is empty.
func (e *Engine) CompleteLines(ctx context.Context, tx pgx.Tx, rid uuid.UUID, lineIDs []uuid.UUID) (Reservation, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status != StatusConfirmed && r.Status != StatusCheckedIn {
		return r, errs.Conflict("invalid_status", "only Confirmed or Checked-in bookings can be completed (is "+r.Status+")")
	}
	ls, err := e.pickLines(r, lineIDs, "checked_in", "confirmed")
	if err != nil {
		return r, err
	}
	if len(ls) == 0 {
		return r, errs.Conflict("nothing_to_complete", "no line of this booking is in play")
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'completed', attributes = attributes || jsonb_build_object('completedAt', now())
		WHERE id = ANY($1)`, ids(ls)); err != nil {
		return r, err
	}
	return e.settle(ctx, tx, r, "completed", "")
}

// NoShowLines marks confirmed lines whose start has passed as No-show and
// frees their court (no refund).
func (e *Engine) NoShowLines(ctx context.Context, tx pgx.Tx, rid uuid.UUID, lineIDs []uuid.UUID, reason string) (Reservation, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status != StatusConfirmed && r.Status != StatusCheckedIn {
		return r, errs.Conflict("invalid_status", "only Confirmed bookings can be marked No-show (is "+r.Status+")")
	}
	ls, err := e.pickLines(r, lineIDs, "confirmed")
	if err != nil {
		return r, err
	}
	now := clock.Now()
	var pick []Line
	for _, l := range ls {
		if l.Start.After(now) {
			if len(lineIDs) > 0 {
				return r, errs.Conflict("not_started", "line "+itoa(l.LineNo)+" has not started yet")
			}
			continue
		}
		pick = append(pick, l)
	}
	if len(pick) == 0 {
		return r, errs.Conflict("nothing_to_mark", "no line of this booking has started without check-in")
	}
	if err := e.release(ctx, tx, rid, ids(pick), "released"); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'no_show' WHERE id = ANY($1)`, ids(pick)); err != nil {
		return r, err
	}
	return e.settle(ctx, tx, r, "no_show", reason)
}

// AddLines adds lines to a confirmed or checked-in booking (overtime, an
// extra hour): every line is allocated at once or none.
func (e *Engine) AddLines(ctx context.Context, tx pgx.Tx, rid uuid.UUID, lines []LineRequest, reason string) (Reservation, []uuid.UUID, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, nil, err
	}
	if r.Status != StatusConfirmed && r.Status != StatusCheckedIn && r.Status != StatusPending {
		return r, nil, errs.Conflict("invalid_status", "lines can only be added to an active booking (is "+r.Status+")")
	}
	next := 0
	for _, l := range r.Lines {
		next = max(next, l.LineNo)
	}
	var added []uuid.UUID
	for _, l := range lines {
		res, err := e.Resource(ctx, tx, l.ResourceID)
		if err != nil {
			return r, nil, err
		}
		if res.PropertyID != r.PropertyID || res.Status != "active" {
			return r, nil, errs.Conflict("resource_inactive", res.Name+" is not available")
		}
		rt, err := e.Type(ctx, tx, res.ResourceType)
		if err != nil {
			return r, nil, err
		}
		if !l.End.After(l.Start) {
			return r, nil, errs.Validation("invalid_period", "end must be after start")
		}
		if l.Quantity <= 0 {
			l.Quantity = 1
		}
		next++
		lid := id.New()
		status := "confirmed"
		if l.Attributes == nil {
			l.Attributes = map[string]any{}
		}
		la := jsonValue(l.Attributes)
		if _, err := tx.Exec(ctx, `INSERT INTO reservation.reservation_lines (id, property_id, reservation_id, line_no, resource_id, resource_type,
			allocation_mode, period, quantity, status, description, attributes, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,tstzrange($8,$9,'[)'),$10,$11,$12,$13,$14)`,
			lid, r.PropertyID, rid, next, res.ID, rt.Code, rt.AllocationMode, l.Start, l.End, l.Quantity, status, nzs(l.Description), la, actor(ctx)); err != nil {
			return r, nil, err
		}
		if err := e.allocate(ctx, tx, r.PropertyID, rid, lid, res, rt, l, false, nil, next); err != nil {
			return r, nil, err
		}
		added = append(added, lid)
	}
	after, err := e.Get(ctx, tx, rid, false)
	if err != nil {
		return after, nil, err
	}
	if err := e.history(ctx, tx, after, "lines_added", r.Status, r.Status, "", reason, map[string]any{"lines": len(added)}); err != nil {
		return after, nil, err
	}
	return after, added, e.publish(ctx, tx, "reservation.rescheduled", after)
}

// Void cancels a booking keyed in by mistake (supervisor, reason required):
// the resources are freed and the charges stay for Finance — no refund is
// made here (Sport Club FR-129).
func (e *Engine) Void(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string) (Reservation, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if !slices.Contains([]string{StatusDraft, StatusPending, StatusConfirmed}, r.Status) {
		return r, errs.Conflict("invalid_status", "a "+r.Status+" booking cannot be voided")
	}
	if err := e.release(ctx, tx, rid, nil, "cancelled"); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'cancelled' WHERE reservation_id = $1`, rid); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservations SET attributes = attributes || jsonb_build_object('void', true, 'voidReason', $2::text,
		'voidedAt', $3::timestamptz) WHERE id = $1`, rid, reason, clock.Now()); err != nil {
		return r, err
	}
	after, err := e.setStatus(ctx, tx, r, StatusCancelled, "voided", reason, `, cancelled_at = now(), cancel_reason = $4`, "void: "+reason)
	if err != nil {
		return after, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "reservation", Action: audit.ActionVoid, EntityType: "reservation.reservation",
		EntityID: rid.String(), EntityLabel: r.Code, PropertyID: &r.PropertyID, Before: r, After: after, Reason: reason}); err != nil {
		return after, err
	}
	return after, e.publish(ctx, tx, "reservation.cancelled", after)
}

// ExpireNow releases a held (Draft) booking at once — a failed online
// payment (Sport Club FR-38): the slots are free again.
func (e *Engine) ExpireNow(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string) (Reservation, error) {
	r, err := e.lock(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status != StatusDraft {
		return r, nil
	}
	if err := e.release(ctx, tx, rid, nil, "released"); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'released' WHERE reservation_id = $1`, rid); err != nil {
		return r, err
	}
	return e.setStatus(ctx, tx, r, StatusExpired, "hold_expired", reason, `, hold_expires_at = least(hold_expires_at, $4)`, clock.Now())
}

// HoldRemaining is the time left of a Draft booking's hold (server clock,
// Sport Club FR-133).
func HoldRemaining(r Reservation) time.Duration {
	if r.Status != StatusDraft || r.HoldExpiresAt == nil {
		return 0
	}
	return max(r.HoldExpiresAt.Sub(clock.Now()), 0)
}

// OpenHours are the bookable hours of a resource on a local day: its own
// opening hours (or the 24-hour demo period), else those of its type;
// closed is true on a day the club is closed.
func (e *Engine) OpenHours(ctx context.Context, q dbtx.Querier, property uuid.UUID, res Resource, rt ResourceType, day time.Time) (time.Time, time.Time, bool, error) {
	loc := calendar.Location(ctx, q)
	d := day.In(loc)
	day = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	kinds, err := calendar.Kinds(ctx, q, property, day)
	if err != nil {
		return day, day, true, err
	}
	if _, closed := kinds["closed"]; closed {
		return day, day, true, nil
	}
	open, close := clockOn(day, rt.OpenTime, loc), clockOn(day, rt.CloseTime, loc)
	if o, c, ok, err := resourceHours(ctx, q, property, res, day, loc); err != nil {
		return open, close, false, err
	} else if ok {
		open, close = o, c
	}
	return open, close, false, nil
}

// NormalHours are the opening hours of a resource on a local day without
// the 24-hour demo period (a booking outside them stays valid when the demo
// ends, Sport Club FR-132).
func (e *Engine) NormalHours(ctx context.Context, q dbtx.Querier, property uuid.UUID, res Resource, rt ResourceType, day time.Time) (time.Time, time.Time, error) {
	attrs := map[string]any{}
	for k, v := range res.Attributes {
		if k != "allDayUntil" {
			attrs[k] = v
		}
	}
	res.Attributes = attrs
	o, c, _, err := e.OpenHours(ctx, q, property, res, rt, day)
	return o, c, err
}
