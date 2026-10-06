package engagement

import (
	"testing"
	"time"
)

// FR-TKT-02: SLA timers count business hours only (08.00–20.00 by default).
func TestAddBusiness(t *testing.T) {
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	pol := DefaultComplaintPolicy
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, jkt) }
	cases := []struct {
		name    string
		start   time.Time
		minutes int
		want    time.Time
	}{
		{"within the day", at(5, 9, 0), 60, at(5, 10, 0)},
		{"before opening starts at 08.00", at(5, 6, 30), 60, at(5, 9, 0)},
		{"after closing continues next morning", at(5, 21, 0), 30, at(6, 8, 30)},
		{"spills over closing", at(5, 19, 30), 60, at(6, 8, 30)},
		{"multi-day", at(5, 8, 0), 2*720 + 15, at(7, 8, 15)},
	}
	for _, c := range cases {
		if got := addBusiness(c.start, c.minutes, pol, jkt); !got.Equal(c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	// Business days: a Monday-to-Friday policy skips the weekend (2026-10-10 is a Saturday).
	weekdays := pol
	weekdays.BusinessDays = []int{1, 2, 3, 4, 5}
	if got := addBusiness(at(9, 19, 0), 120, weekdays, jkt); !got.Equal(at(12, 9, 0)) {
		t.Errorf("weekend skipped: got %v", got)
	}
}
