package golf

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
)

// zoneTx answers the timezone lookup (Asia/Jakarta, UTC+7) and nothing else.
type zoneTx struct{ pgx.Tx }

type zoneRow struct{ sql string }

func (zoneTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row { return zoneRow{sql} }

func (r zoneRow) Scan(dest ...any) error {
	if s, ok := dest[0].(*string); ok && strings.Contains(r.sql, "timezone") {
		*s = "Asia/Jakarta"
		return nil
	}
	return pgx.ErrNoRows
}

// 18:30 UTC on 5 October is 01:30 WIB on 6 October: the club's date is a day
// ahead of the UTC date.
func pinEveningUTC(t *testing.T) {
	clock.SetOffsetForTest(time.Until(time.Date(2026, 10, 5, 18, 30, 0, 0, time.UTC)))
	t.Cleanup(func() { clock.SetOffsetForTest(0) })
}

func TestTemplateEffectiveOnClubDate(t *testing.T) {
	pinEveningUTC(t)
	before := map[string]any{"effectiveFrom": "2026-10-06", "startTime": "06:00", "endTime": "08:00"}
	err := templateBeforeWrite(context.Background(), zoneTx{}, map[string]any{"intervalMinutes": int64(10)}, before)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != "template_already_effective" {
		t.Fatalf("a template effective from the club's today (6 Oct) must not be edited at 01:30 WIB, got %v", err)
	}
	before["effectiveFrom"] = "2026-10-07"
	if err := templateBeforeWrite(context.Background(), zoneTx{}, map[string]any{"intervalMinutes": int64(10)}, before); err != nil {
		t.Fatalf("a template effective tomorrow is editable: %v", err)
	}
}
