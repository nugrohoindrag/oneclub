package commercial

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

// zoneTx answers the timezone lookup (Asia/Jakarta, UTC+7); every other
// query finds no rows.
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

func TestRuleVersionEffectiveOnClubDate(t *testing.T) {
	pinEveningUTC(t)
	for name, hook := range map[string]func(context.Context, pgx.Tx, map[string]any, map[string]any) error{
		"golf rule": ruleBeforeWrite, "line rule": lineRuleVersion} {
		before := map[string]any{"code": "GF", "effectiveFrom": "2026-10-06", "status": "active"}
		err := hook(context.Background(), zoneTx{}, map[string]any{"price": "100000"}, before)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != "rule_already_effective" {
			t.Errorf("%s: a version effective from the club's today (6 Oct) is immutable at 01:30 WIB, got %v", name, err)
		}
	}
}

func TestRatePlanDefaultsToClubDate(t *testing.T) {
	pinEveningUTC(t)
	v := map[string]any{}
	if err := RatePlans.Hooks.BeforeWrite(context.Background(), zoneTx{}, v, nil); err != nil {
		t.Fatal(err)
	}
	if v["effectiveFrom"] != "2026-10-06" {
		t.Fatalf("a new rate plan is effective from the club's today (2026-10-06), got %v", v["effectiveFrom"])
	}
}
