package reporting

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
)

var errCaptured = errors.New("captured")

// captureTx answers the timezone lookup (Asia/Jakarta, UTC+7) and records
// the report query instead of running it.
type captureTx struct {
	pgx.Tx
	sql  string
	args []any
}

type zoneRow struct{ sql string }

func (r zoneRow) Scan(dest ...any) error {
	if s, ok := dest[0].(*string); ok && strings.Contains(r.sql, "timezone") {
		*s = "Asia/Jakarta"
		return nil
	}
	return errCaptured
}

func (c *captureTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if !strings.Contains(sql, "timezone") {
		c.sql, c.args = sql, args
	}
	return zoneRow{sql}
}

func (c *captureTx) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.sql, c.args = sql, args
	return nil, errCaptured
}

// 18:30 UTC on 5 October is 01:30 WIB on 6 October: the club's date is a day
// ahead of the UTC date.
func pinEveningUTC(t *testing.T) {
	clock.SetOffsetForTest(time.Until(time.Date(2026, 10, 5, 18, 30, 0, 0, time.UTC)))
	t.Cleanup(func() { clock.SetOffsetForTest(0) })
}

func TestReportsDefaultToClubDate(t *testing.T) {
	pinEveningUTC(t)
	for _, r := range []*Report{DailyTeeSheetReport, ExpiringMembershipReport} {
		tx := &captureTx{}
		if _, err := r.Query(context.Background(), tx, map[string]string{}, 10); !errors.Is(err, errCaptured) {
			t.Fatalf("%s: %v", r.Code, err)
		}
		if !slices.Contains(tx.args, any("2026-10-06")) || strings.Contains(tx.sql, "current_date") {
			t.Errorf("%s runs on the club's today (2026-10-06), not the UTC date: %s %v", r.Code, tx.sql, tx.args)
		}
	}
}

// A timestamp's day is its day at the club: ts::date is its UTC date, the
// previous day from 00:00 to 07:00 WIB, and ts < ($2::date + 1) cuts at
// midnight UTC (07:00 WIB) instead of the club's midnight (as-of reports).
var utcDate = regexp.MustCompile(`\b[a-z_]+_at\)?::date|current_date|\b[a-z_]+_at\)?\s*(<|<=|>|>=)\s*\(?\$\d+::date([^:\w]|$)`)

func TestReportsDateTimestampsAtTheClub(t *testing.T) {
	pinEveningUTC(t)
	reports := slices.Concat(P1Reports, P2Reports, P3Reports(), P4Reports(), P5Reports(), LeisureReports())
	for _, r := range reports {
		tx := &captureTx{}
		if _, err := r.Query(context.Background(), tx, map[string]string{"from": "2026-10-01", "to": "2026-10-06", "date": "2026-10-06"}, 10); !errors.Is(err, errCaptured) {
			t.Fatalf("%s: %v", r.Code, err)
		}
		if m := utcDate.FindString(tx.sql); m != "" {
			t.Errorf("%s compares %s with a club date", r.Code, m)
		}
	}
	tx := &captureTx{}
	if _, err := GolfExecutive(context.Background(), tx, time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)); !errors.Is(err, errCaptured) {
		t.Fatal(err)
	}
	if m := utcDate.FindString(tx.sql); m != "" || !slices.Contains(tx.args, any("Asia/Jakarta")) {
		t.Errorf("golf revenue of the day compares %s with a club date: %v", m, tx.args)
	}
}
