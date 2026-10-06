package analytics

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestRecencyScore(t *testing.T) {
	breaks := DefaultSegmentationPolicy.RecencyBreaks // 14, 30, 60, 120
	for _, c := range []struct{ days, want int }{{0, 5}, {14, 5}, {15, 4}, {30, 4}, {45, 3}, {60, 3}, {61, 2}, {120, 2}, {121, 1}, {400, 1}} {
		if got := RecencyScore(c.days, breaks); got != c.want {
			t.Errorf("%d days: %d, want %d", c.days, got, c.want)
		}
	}
}

func TestQuintileScores(t *testing.T) {
	var v []decimal.Decimal
	for i := 1; i <= 10; i++ {
		v = append(v, decimal.NewFromInt(int64(i)))
	}
	got := QuintileScores(v)
	want := []int{1, 1, 1, 2, 2, 3, 3, 4, 4, 5}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("quintiles %v, want %v", got, want)
		}
	}
	// Ties share a score; order of input does not matter.
	tie := QuintileScores([]decimal.Decimal{decimal.NewFromInt(5), decimal.NewFromInt(1), decimal.NewFromInt(5), decimal.NewFromInt(5)})
	if tie[0] != tie[2] || tie[0] != tie[3] || tie[1] != 1 {
		t.Errorf("ties: %v", tie)
	}
	if two := QuintileScores([]decimal.Decimal{decimal.NewFromInt(1), decimal.NewFromInt(9)}); two[0] != 1 || two[1] != 5 {
		t.Errorf("two values: %v", two)
	}
	if one := QuintileScores([]decimal.Decimal{decimal.NewFromInt(7)}); one[0] != 3 {
		t.Errorf("one value: %v", one)
	}
	if len(QuintileScores(nil)) != 0 {
		t.Error("empty")
	}
}

func TestRFMGroup(t *testing.T) {
	for _, c := range []struct {
		r, f, m, days int
		want          string
	}{
		{5, 5, 5, 3, "champions"},
		{4, 4, 4, 20, "champions"},
		{3, 4, 2, 50, "loyal"},
		{3, 3, 3, 45, "loyal"},
		{2, 4, 4, 90, "at_risk"}, // PRD P5 §9.4: good customer, 60+ days without a visit
		{1, 3, 1, 150, "at_risk"},
		{5, 1, 2, 2, "new"},
		{1, 1, 1, 150, "lapsed"},
		{2, 5, 5, 200, "lapsed"}, // beyond the lapsed days
		{3, 2, 2, 40, "potential"},
	} {
		if got := RFMGroup(c.r, c.f, c.m, c.days, 180); got != c.want {
			t.Errorf("R%d F%d M%d %dd: %s, want %s", c.r, c.f, c.m, c.days, got, c.want)
		}
	}
}

func TestCLV(t *testing.T) {
	// Rp36,5 jt over 365 days, 3 years → Rp109,5 jt.
	if got := CLV(decimal.RequireFromString("36500000"), 365, decimal.NewFromInt(3)); !got.Equal(decimal.RequireFromString("109500000")) {
		t.Errorf("CLV %s", got)
	}
	// Half a year window doubles the yearly spend.
	if got := CLV(decimal.RequireFromString("1000"), 182, decimal.NewFromInt(1)); !got.Equal(decimal.RequireFromString("2005.49")) {
		t.Errorf("CLV %s", got)
	}
	if !CLV(decimal.Zero, 365, decimal.NewFromInt(3)).IsZero() {
		t.Error("no spend")
	}
}

func TestParsePeriod(t *testing.T) {
	today := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	p, err := ParsePeriod("", "", today, 90)
	if err != nil || !p.To.Equal(today) || !p.From.Equal(today.AddDate(0, 0, -90)) {
		t.Errorf("default: %v %v", p, err)
	}
	if _, err := ParsePeriod("2026-10-05", "2026-10-01", today, 90); err == nil {
		t.Error("inverted period")
	}
	if _, err := ParsePeriod("05/10/2026", "", today, 90); err == nil {
		t.Error("bad date")
	}
}
