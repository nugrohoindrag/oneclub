// Package calendar answers date questions of every business line from P1's
// Day Calendar (platform.calendar_days, PRD P1 §9): public holidays used by
// day types (PRD P2 EP-02), special dates and closures (Reservation Engine),
// and the local timezone.
package calendar

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/org"
)

// Kinds returns the active kinds of a local date at the property
// (instance-wide entries included): kind → name.
func Kinds(ctx context.Context, q dbtx.Querier, property uuid.UUID, date time.Time) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT kind, name FROM platform.calendar_days WHERE day = $2::date AND status = 'active'
		AND (property_id IS NULL OR property_id = $1)`, property, date.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, n string
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// IsHoliday reports whether date is a public holiday at the property.
func IsHoliday(ctx context.Context, q dbtx.Querier, property uuid.UUID, date time.Time) (bool, error) {
	kind, _, _, found, err := org.CalendarEntry(ctx, q, property, date)
	return found && kind == "public_holiday", err
}

// Location returns the instance timezone (all local-date logic uses it).
func Location(ctx context.Context, q dbtx.Querier) *time.Location {
	loc, err := org.Location(ctx, q, uuid.Nil)
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}
