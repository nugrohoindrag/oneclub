package banquet

// Package cancellation (PRD P3 FR-PKG-08): the event created from the
// banquet component of a package follows commercial.package_cancelled —
// cancelled with it (the package cancellation policy already settled the
// money, so nothing is forfeited on the event) or, under Banquet Policies
// "tentative", back to Tentative with a new option date so the customer can
// keep the date standalone; the option expiry releases it otherwise.
// Decoded by name: banquet never imports the package engine.

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/outbox"
)

// PackageCancelled is the package event consumed (docs/p3-p4-contracts.md).
const PackageCancelled = "commercial.package_cancelled"

// OnPackageCancelled cancels (or returns to Tentative) the open events of
// a cancelled package booking.
func (m *Module) OnPackageCancelled(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p struct {
		BookingID uuid.UUID `json:"bookingId"`
		Number    string    `json:"number"`
		Status    string    `json:"status"`
		Reason    string    `json:"reason"`
	}
	if err := ev.Decode(&p); err != nil || p.BookingID == uuid.Nil {
		return nil //nolint:nilerr // foreign payload
	}
	property := eventProperty(uuid.Nil, ev)
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	rows, err := tx.Query(ctx, `SELECT id FROM banquet.events WHERE package_booking_id = $1 AND status IN ('inquiry', 'tentative', 'definite') ORDER BY start_at`,
		p.BookingID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(ids) == 0 {
		return err
	}
	pol, _, err := bookingPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	status := strings.TrimSpace(p.Status)
	if status == "" {
		status = "cancelled"
	}
	reason := "package " + p.Number + " " + status
	if p.Reason != "" {
		reason += ": " + p.Reason
	}
	for _, eid := range ids {
		if pol.PackageCancellation == "tentative" {
			if err := m.backToTentative(ctx, tx, eid, pol, reason); err != nil {
				return err
			}
			continue
		}
		if _, err := m.CancelEvent(ctx, tx, eid, EventCancelInput{Reason: reason, WaiveFee: true}); err != nil {
			return err
		}
	}
	return nil
}

// backToTentative turns a Definite event back into a Tentative one: its
// venues become tentative holds with a new option date (Banquet Policies).
func (m *Module) backToTentative(ctx context.Context, tx pgx.Tx, eid uuid.UUID, pol BookingPolicy, reason string) error {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return err
	}
	if e.Status != StatusDefinite {
		return nil
	}
	opt := optionUntil(pol, e.Start)
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_venues SET status = 'tentative', option_date = $2 WHERE event_id = $1 AND status = 'definite'`,
		eid, opt); err != nil {
		return err
	}
	_, err = m.setStatus(ctx, tx, e, StatusTentative, "package_cancelled", reason, `, option_date = $4`, opt)
	return err
}
