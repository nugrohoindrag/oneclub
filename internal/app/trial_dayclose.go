package app

// Trial dataset: the business day frame. Each simulated day the front desk
// cashier opens a cashier shift in the morning; at night the shift is
// counted and closed (now and then a small variance) and the Finance
// Manager runs the Night Audit, which closes the business day, freezes the
// Daily Revenue Report and lets accounting post the daily journal (K4).

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

const (
	trialCashier = "cashier@demo.oneclub.id"
	trialFinance = "finance@demo.oneclub.id"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "day-open", Order: 5, Day: trialDayOpen})
	registerTrialSeeder(trialSeeder{Name: "day-close", Order: 900, Day: trialDayClose})
}

func trialDayOpen(_ context.Context, t *Trial, day time.Time) error {
	t.At(day, "06:00")
	c := t.As(trialCashier)
	if st, cur, _ := c.Call("GET", "/api/v1/billing/cashier-shifts/current", nil); st == 200 && cur.S("id") != "" {
		return nil
	}
	c.Post("/api/v1/billing/cashier-shifts", J{"station": "front_desk", "openingFloat": "1000000"})
	return nil
}

func trialDayClose(ctx context.Context, t *Trial, day time.Time) error {
	t.At(day, "22:30")
	t.ApprovePending(ctx)
	c := t.As(trialCashier)
	if st, cur, _ := c.Call("GET", "/api/v1/billing/cashier-shifts/current", nil); st == 200 && cur.S("id") != "" {
		expected, _ := decimal.NewFromString(cur.S("expected"))
		counted, note := expected, "Cash counted, no variance"
		if r := t.Rand("close:" + day.Format(time.DateOnly)); r.IntN(10) == 0 {
			counted, note = expected.Sub(decimal.NewFromInt(int64(r.IntN(5)+1)*5000)), "Small cash shortage, reported to the supervisor"
		}
		c.Post("/api/v1/billing/cashier-shifts/"+cur.S("id")+":close", J{"countedCash": counted.String(), "note": note})
	}
	t.At(day, "23:45")
	run := t.As(trialFinance).Post("/api/v1/billing/business-days:night-audit", nil)
	if run.S("status") != "completed" || run.S("businessDate") != day.Format(time.DateOnly) {
		return fmt.Errorf("night audit of %s: %s %v", day.Format(time.DateOnly), run.S("status"), run["checks"])
	}
	return nil
}
