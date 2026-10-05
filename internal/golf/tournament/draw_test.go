package tournament

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/golf"
)

func route18() []golf.RouteHole {
	pars := []int{4, 4, 3, 5, 4, 4, 3, 4, 5, 4, 4, 3, 5, 4, 4, 3, 4, 5}
	out := make([]golf.RouteHole, len(pars))
	for i, p := range pars {
		out[i] = golf.RouteHole{Sequence: i + 1, Number: i + 1, Par: p}
	}
	return out
}

// FR-TRN-06: a shotgun puts one flight per hole; above 18 flights A/B
// groups, the B groups first on par 5s and par 4s.
func TestShotgunStarts(t *testing.T) {
	tr := Tournament{StartType: "shotgun"}
	r := TournamentRound{PlayDate: "2026-11-14", StartTime: "07:00"}
	s, err := assignStarts(tr, r, route18(), 18, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, x := range s {
		if x.group != nil || seen[x.hole] || x.at.Hour() != 7 {
			t.Fatalf("18 flights: %+v", s)
		}
		seen[x.hole] = true
	}
	s, err = assignStarts(tr, r, route18(), 20, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if *s[0].group != "A" || *s[18].group != "B" || s[18].hole != 4 || s[19].hole != 9 {
		t.Fatalf("A/B groups: first B on hole %d, %d", s[18].hole, s[19].hole)
	}
	if _, err := assignStarts(tr, r, route18(), 37, time.UTC); err == nil {
		t.Fatal("more than 36 flights cannot start by shotgun")
	}
	tt := Tournament{StartType: "tee_times"}
	two := TournamentRound{PlayDate: "2026-11-14", StartTime: "07:00", StartTees: "1,10", TeeIntervalMinutes: 10}
	s, err = assignStarts(tt, two, route18(), 4, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if s[0].hole != 1 || s[1].hole != 10 || !s[0].at.Equal(s[1].at) || s[2].at.Sub(s[0].at) != 10*time.Minute {
		t.Fatalf("two-tee start: %+v", s)
	}
}

// FR-TRN-05: balanced flights, pairing groups kept together.
func TestBuildFlights(t *testing.T) {
	var ps []drawPlayer
	for i := 0; i < 10; i++ {
		p := drawPlayer{id: uuid.New()}
		if i == 1 || i == 6 {
			p.group = "SPONSOR"
		}
		ps = append(ps, p)
	}
	fs := buildFlights(ps, 4, true)
	if len(fs) != 3 {
		t.Fatalf("flights: %d", len(fs))
	}
	total := 0
	for _, f := range fs {
		total += len(f)
		if len(f) < 3 || len(f) > 4 {
			t.Fatalf("unbalanced: %d", len(f))
		}
	}
	if total != 10 {
		t.Fatalf("players: %d", total)
	}
	found := false
	for _, f := range fs {
		n := 0
		for _, p := range f {
			if p.group == "SPONSOR" {
				n++
			}
		}
		found = found || n == 2
	}
	if !found {
		t.Fatal("pairing group split")
	}
}

// WHS course handicap and the playing handicap after the allowance.
func TestHandicaps(t *testing.T) {
	idx, cr, slope := "18.2", "72.0", 130
	c, p := handicaps(&idx, decimal.NewFromInt(36), &cr, &slope, 72, 18, decimal.NewFromInt(95))
	if *c != 21 || *p != 20 {
		t.Fatalf("course %d playing %d", *c, *p)
	}
	high := "45"
	c, _ = handicaps(&high, decimal.NewFromInt(36), &cr, &slope, 72, 18, decimal.NewFromInt(100))
	if *c != 41 { // capped at 36 × 130 / 113
		t.Fatalf("capped course handicap %d", *c)
	}
	if c, p := handicaps(nil, decimal.Zero, nil, nil, 72, 18, decimal.NewFromInt(95)); c != nil || p != nil {
		t.Fatal("no index, no handicap")
	}
}
