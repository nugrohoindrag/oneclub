package experience

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCourseHandicap(t *testing.T) {
	cr := "72"
	for _, c := range []struct {
		index       string
		slope, want int
	}{
		// Handicap Index tables of Modern Golf & Country Club (course rating = par 72).
		{"10.6", 134, 13}, {"11.3", 134, 13}, {"11.4", 134, 14}, // Black
		{"10.8", 132, 13}, {"11.5", 132, 13}, // Blue / Red
		{"0.5", 130, 1}, {"1.3", 130, 1}, {"1.4", 130, 2}, // White
		{"-0.5", 134, -1}, // plus handicap
	} {
		s := c.slope
		if got := CourseHandicap(decimal.RequireFromString(c.index), &cr, &s, 72, 18); got != c.want {
			t.Errorf("index %s slope %d: got %d, want %d", c.index, c.slope, got, c.want)
		}
	}
	// nine holes: the 18-hole rating and index are halved against the nine-hole par.
	s := 130
	if got := CourseHandicap(decimal.RequireFromString("18.0"), &cr, &s, 36, 9); got != 10 {
		t.Errorf("nine holes: got %d, want 10", got)
	}
	// no tee set: the index rounded.
	if got := CourseHandicap(decimal.RequireFromString("7.6"), nil, nil, 72, 18); got != 8 {
		t.Errorf("no tee set: got %d, want 8", got)
	}
}

func TestHoleStrokes(t *testing.T) {
	si := func(v ...int) []*int {
		out := make([]*int, len(v))
		for i := range v {
			out[i] = &v[i]
		}
		return out
	}
	front := si(15, 3, 9, 17, 7, 11, 1, 5, 13)
	sum := func(s []int) (n int) {
		for _, v := range s {
			n += v
		}
		return n
	}
	got := HoleStrokes(3, front) // hardest three: SI 1, 3, 5 → holes 7, 2, 8
	if got[6] != 1 || got[1] != 1 || got[7] != 1 || sum(got) != 3 {
		t.Errorf("3 strokes: %v", got)
	}
	got = HoleStrokes(11, front) // one on every hole, two more on SI 1 and 3
	if got[6] != 2 || got[1] != 2 || got[0] != 1 || sum(got) != 11 {
		t.Errorf("11 strokes: %v", got)
	}
	got = HoleStrokes(-1, front) // plus one: gives a stroke back on the easiest hole (SI 17)
	if got[3] != -1 || sum(got) != -1 {
		t.Errorf("plus 1: %v", got)
	}
}
