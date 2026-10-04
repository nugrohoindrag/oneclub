package reservation

import (
	"context"
	"time"

	"github.com/google/uuid"

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
