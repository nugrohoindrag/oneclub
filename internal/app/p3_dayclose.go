package app

// End-of-Day checks contributed to the billing Night Audit (PRD P3 FR-EOD-02,
// FR-EOD-03): open POS shifts block the close; offline transactions rejected
// or in conflict and bookings neither checked in nor marked no-show are
// warnings for the night auditor. Billing sits below POS, golf and the
// reservation engine (Technical Doc §4.2), so the composition root plugs the
// checks in.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/platform/org"
)

func (a *App) registerNightAuditChecks() {
	a.Billing.RegisterNightAuditCheck("open_pos_shifts", nightAuditPOSShifts)
	a.Billing.RegisterNightAuditCheck("offline_transactions", nightAuditOffline)
	a.Billing.RegisterNightAuditCheck("unresolved_bookings", nightAuditBookings)
}

func auditList(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func auditSeverity(hit bool, s string) string {
	if hit {
		return s
	}
	return "info"
}

// dayBounds is the local calendar day of a business date.
func dayBounds(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) (time.Time, time.Time) {
	loc, err := org.Location(ctx, tx, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 0, 1)
}

func nightAuditPOSShifts(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) ([]billing.AuditFinding, error) {
	_, to := dayBounds(ctx, tx, property, day)
	items, err := auditList(ctx, tx, `SELECT s.shift_no || ' · ' || o.name FROM commercial.pos_shifts s JOIN commercial.outlets o ON o.id = s.outlet_id
		WHERE s.property_id = $1 AND s.status = 'open' AND s.opened_at < $2 ORDER BY s.opened_at LIMIT 50`, property, to)
	return []billing.AuditFinding{{Check: "open_pos_shifts", Severity: auditSeverity(len(items) > 0, "blocking"), Count: len(items),
		Message: "POS shifts must be closed before the business day is closed", Items: items}}, err
}

func nightAuditOffline(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) ([]billing.AuditFinding, error) {
	from, to := dayBounds(ctx, tx, property, day)
	items, err := auditList(ctx, tx, `SELECT action || ' · ' || status FROM platform.sync_items WHERE property_id = $1 AND status IN ('rejected', 'conflict')
		AND coalesce(client_time, received_at) >= $2 AND coalesce(client_time, received_at) < $3 ORDER BY received_at LIMIT 50`, property, from, to)
	return []billing.AuditFinding{{Check: "offline_transactions", Severity: auditSeverity(len(items) > 0, "warning"), Count: len(items),
		Message: "Offline transactions of the day rejected or in conflict at sync; review them", Items: items}}, err
}

func nightAuditBookings(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) ([]billing.AuditFinding, error) {
	_, to := dayBounds(ctx, tx, property, day)
	golf, err := auditList(ctx, tx, `SELECT 'Golf ' || code FROM golf.bookings WHERE property_id = $1 AND status IN ('pending', 'confirmed')
		AND play_date <= $2::date ORDER BY play_date, code LIMIT 50`, property, day.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	other, err := auditList(ctx, tx, `SELECT DISTINCT r.code FROM reservation.reservations r JOIN reservation.reservation_lines l ON l.reservation_id = r.id
		WHERE r.property_id = $1 AND r.status IN ('pending', 'confirmed') AND l.status IN ('held', 'confirmed') AND lower(l.period) < $2
		ORDER BY r.code LIMIT 50`, property, to)
	if err != nil {
		return nil, err
	}
	items := append(golf, other...)
	return []billing.AuditFinding{{Check: "unresolved_bookings", Severity: auditSeverity(len(items) > 0, "warning"), Count: len(items),
		Message: "Bookings of the day neither checked in nor marked no-show", Items: items}}, nil
}
