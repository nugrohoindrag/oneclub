package integration

import (
	"context"
	"testing"
	"time"

	"oneclub/internal/kernel/clock"
)

// The sandbox gateway settles up to the club's yesterday, by club dates: at
// 18:30 UTC on 5 October it is 01:30 WIB on 6 October, so 5 October is
// settled and 6 October still pending (the UTC date was a day behind).
func TestMockSettlementUsesTheClubDays(t *testing.T) {
	clock.SetOffsetForTest(time.Until(time.Date(2026, 10, 5, 18, 30, 0, 0, time.UTC)))
	t.Cleanup(func() { clock.SetOffsetForTest(0) })
	wib, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	from := time.Date(2026, 10, 5, 0, 0, 0, 0, wib)
	items, err := (&mockPayment{}).SettlementDetails(context.Background(), from, from.AddDate(0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, it := range items {
		status[it.Reference[4:12]] = it.Status
	}
	if len(items) != 6 || status["20261005"] != "settled" || status["20261006"] != "pending" {
		t.Fatalf("club days 5 Oct settled, 6 Oct pending: %v", status)
	}
}
