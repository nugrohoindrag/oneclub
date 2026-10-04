package reservation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
)

// ActiveStay returns the Checked-in stay reservation of a customer covering
// time at (staying guests' facility access, FR-SPT-09 / FR-STY-08).
func (e *Engine) ActiveStay(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID, at time.Time) (*Reservation, error) {
	rows, err := q.Query(ctx, reservationSelect+` WHERE r.property_id = $1 AND r.customer_id = $2 AND r.business_line = 'stay' AND r.status = 'checked_in'
		AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND l.period @> $3::timestamptz)
		ORDER BY r.checked_in_at DESC LIMIT 1`, property, customerID, at)
	res, err := handle.One[Reservation](rows, err, "stay")
	if errs.Is(err, errs.KindNotFound) {
		return nil, nil
	}
	return &res, err
}

// ByCode returns a reservation by its code (booking QR / confirmation).
func (e *Engine) ByCode(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) (*Reservation, error) {
	var rid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM reservation.reservations WHERE property_id = $1 AND code = upper($2)`, property, code).Scan(&rid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r, err := e.Get(ctx, q, rid, false)
	return &r, err
}

// IDsInRange lists the bookings of a business line and source with a line in
// a period (status optional), newest first.
func (e *Engine) IDsInRange(ctx context.Context, q dbtx.Querier, property uuid.UUID, line, sourceType, status string, from, to time.Time, limit int) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT r.id FROM reservation.reservations r WHERE r.property_id = $1 AND r.business_line = $2 AND r.kind = 'booking'
		AND r.source_type = $3 AND ($4 = '' OR r.status = $4)
		AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND l.period && tstzrange($5, $6, '[)'))
		ORDER BY r.created_at DESC LIMIT $7`, property, line, sourceType, status, from, to, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// SetSource records the business document of a reservation.
func (e *Engine) SetSource(ctx context.Context, tx pgx.Tx, rid, sourceID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE reservation.reservations SET source_id = $2 WHERE id = $1`, rid, sourceID)
	return err
}

// SetAttribute stores one attribute of a reservation.
func (e *Engine) SetAttribute(ctx context.Context, tx pgx.Tx, rid uuid.UUID, key string, value any) error {
	_, err := tx.Exec(ctx, `UPDATE reservation.reservations SET attributes = attributes || jsonb_build_object($2::text, $3::jsonb) WHERE id = $1`,
		rid, key, jsonValue(value))
	return err
}

// SetPolicyRefs records the policy versions a reservation was made under.
func (e *Engine) SetPolicyRefs(ctx context.Context, tx pgx.Tx, rid uuid.UUID, refs any) error {
	_, err := tx.Exec(ctx, `UPDATE reservation.reservations SET policy_refs = $2 WHERE id = $1`, rid, jsonValue(refs))
	return err
}

// BookedAt is the booked places of the capacity slot of a resource at a time.
func (e *Engine) BookedAt(ctx context.Context, q dbtx.Querier, resourceID uuid.UUID, at time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT coalesce((SELECT booked FROM reservation.capacity_slots WHERE resource_id = $1 AND period @> $2::timestamptz LIMIT 1), 0)`,
		resourceID, at).Scan(&n)
	return n, err
}

// DeleteSlot removes the capacity slot of a source when nobody booked it
// (regenerated class schedule); it reports whether the slot is gone.
func (e *Engine) DeleteSlot(ctx context.Context, tx pgx.Tx, sourceType string, sourceID uuid.UUID) (bool, error) {
	var booked int
	err := tx.QueryRow(ctx, `SELECT booked FROM reservation.capacity_slots WHERE source_type = $1 AND source_id = $2`, sourceType, sourceID).Scan(&booked)
	if dbtx.IsNoRows(err) {
		return true, nil
	}
	if err != nil || booked > 0 {
		return false, err
	}
	_, err = tx.Exec(ctx, `DELETE FROM reservation.capacity_slots WHERE source_type = $1 AND source_id = $2 AND booked = 0`, sourceType, sourceID)
	return err == nil, err
}

// BusyResources counts how many of the resources hold a held or confirmed
// exclusive allocation overlapping [from, to) (stay availability).
func (e *Engine) BusyResources(ctx context.Context, q dbtx.Querier, resourceIDs []uuid.UUID, from, to time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(DISTINCT resource_id) FROM reservation.allocations WHERE resource_id = ANY($1)
		AND status IN ('held', 'confirmed') AND period && tstzrange($2, $3, '[)')`, resourceIDs, from, to).Scan(&n)
	return n, err
}

// LineResources are the resources booked by a reservation.
func (e *Engine) LineResources(ctx context.Context, q dbtx.Querier, rid uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT resource_id FROM reservation.reservation_lines WHERE reservation_id = $1`, rid)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

func jsonValue(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
