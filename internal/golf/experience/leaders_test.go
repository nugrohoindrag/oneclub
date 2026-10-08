package experience

import (
	"testing"
	"time"
)

func TestPeriodOf(t *testing.T) {
	day := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC) // a Friday
	from, to, err := periodOf("week", day)
	if err != nil || from.Format("2006-01-02") != "2026-10-05" || to.Format("2006-01-02") != "2026-10-11" {
		t.Fatalf("week: %v – %v (%v)", from, to, err)
	}
	sunday := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	if from, _, _ := periodOf("week", sunday); from.Format("2006-01-02") != "2026-10-05" {
		t.Fatalf("Sunday belongs to the week from Monday: %v", from)
	}
	from, to, _ = periodOf("month", day)
	if from.Format("2006-01-02") != "2026-10-01" || to.Format("2006-01-02") != "2026-10-31" {
		t.Fatalf("month: %v – %v", from, to)
	}
	if _, _, err := periodOf("year", day); err == nil {
		t.Fatal("unknown period accepted")
	}
}
