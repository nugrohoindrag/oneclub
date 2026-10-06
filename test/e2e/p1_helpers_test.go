package e2e

// Helpers of the P1 Golf Core MVP end-to-end tests.

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/app"
	"oneclub/internal/kernel/dbtx"
)

// OK asserts a 2xx status (200, 201, 202 or 204).
func (c *Client) OK(method, path string, body any, hdr ...string) Resp {
	c.t.Helper()
	r := c.Do(method, path, body, hdr...)
	if r.Status < 200 || r.Status > 299 {
		c.t.Fatalf("%s %s: want 2xx, got %s", method, path, r.String())
	}
	return r
}

// sysExec runs a statement with an all-properties scope (test set-up only).
func sysExec(t testing.TB, in *Instance, sql string, args ...any) {
	t.Helper()
	ctx := dbtx.System(context.Background())
	if err := in.DB.WithTx(ctx, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, args...); return err }); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func clubLoc(in *Instance) *time.Location { return in.App.Instance.Location() }

// clubDay returns the first local date on or after today+minDays whose
// weekday satisfies ok.
func clubDay(in *Instance, minDays int, ok func(time.Weekday) bool) string {
	d := time.Now().In(clubLoc(in)).AddDate(0, 0, minDays)
	for !ok(d.Weekday()) {
		d = d.AddDate(0, 0, 1)
	}
	return d.Format("2006-01-02")
}

// clubToday is the local date of the club (Asia/Jakarta), not the UTC date:
// they differ from 17:00 to 24:00 UTC.
func clubToday(in *Instance) string { return time.Now().In(clubLoc(in)).Format("2006-01-02") }

// clubDateAgo is the local club date years/months/days before today.
func clubDateAgo(in *Instance, years, months, days int) string {
	return time.Now().In(clubLoc(in)).AddDate(-years, -months, -days).Format("2006-01-02")
}

func isWeekday(w time.Weekday) bool  { return w != time.Saturday && w != time.Sunday }
func isSaturday(w time.Weekday) bool { return w == time.Saturday }

func demoCourse(t testing.TB, in *Instance) string {
	var id uuid.UUID
	sysQueryRow(t, in, `SELECT id FROM golf.courses WHERE property_id = $1 AND code = $2`, []any{in.Main, app.DemoCourseCode}, &id)
	return id.String()
}

func demoMemberCustomer(t testing.TB, in *Instance) string {
	var id uuid.UUID
	sysQueryRow(t, in, `SELECT customer_id FROM membership.members WHERE property_id = $1 AND code = $2`, []any{in.Main, app.DemoMemberCode}, &id)
	return id.String()
}

// teeTimes generates the tee sheet of day and returns its slots sorted by
// start time then tee.
func teeTimes(t testing.TB, c *Client, course, day string) []map[string]any {
	t.Helper()
	c.OK("POST", "/api/v1/golf/tee-sheets:generate", map[string]any{"courseId": course, "from": day, "days": 1})
	items := c.Must(200, "GET", "/api/v1/golf/tee-times?date="+day+"&courseId="+course, nil).Items()
	sort.SliceStable(items, func(i, j int) bool {
		a, b := str(items[i]["startAt"]), str(items[j]["startAt"])
		if a != b {
			return a < b
		}
		return items[i]["startTee"].(float64) < items[j]["startTee"].(float64)
	})
	return items
}

// slotsOf filters slots by session and start tee.
func slotsOf(slots []map[string]any, session string, tee int) []map[string]any {
	var out []map[string]any
	for _, s := range slots {
		if s["session"] == session && int(s["startTee"].(float64)) == tee {
			out = append(out, s)
		}
	}
	return out
}

func dec(v any) decimal.Decimal {
	d, _ := decimal.NewFromString(strings.TrimSpace(str(v)))
	return d
}

func eqAmount(t testing.TB, what string, got any, want int64) {
	t.Helper()
	if !dec(got).Equal(decimal.NewFromInt(want)) {
		t.Fatalf("%s: want %d, got %v", what, want, got)
	}
}

// presentCaddies records every caddy present on day and returns their ids.
func presentCaddies(t testing.TB, c *Client, day string) []string {
	t.Helper()
	cs := c.Must(200, "GET", "/api/v1/golf/caddies?limit=100", nil).Items()
	var entries []map[string]any
	var ids []string
	for _, x := range cs {
		if x["status"] == "active" {
			entries = append(entries, map[string]any{"caddyId": x["id"], "status": "present"})
			ids = append(ids, str(x["id"]))
		}
	}
	c.Must(200, "PUT", "/api/v1/golf/caddy-availability", map[string]any{"date": day, "entries": entries})
	return ids
}

// payFolio settles the balance of a booking folio in cash.
func payFolio(t testing.TB, c *Client, booking map[string]any) map[string]any {
	t.Helper()
	folio := booking["folio"].(map[string]any)
	return c.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": booking["folioId"], "amount": str(folio["balance"]),
		"methodType": "cash", "channel": "venue"}).JSON()
}

func playerIDs(b map[string]any) []string {
	var out []string
	for _, p := range b["players"].([]any) {
		out = append(out, str(p.(map[string]any)["id"]))
	}
	return out
}

func firstFlight(b map[string]any) string {
	return str(b["flights"].([]any)[0].(map[string]any)["id"])
}
