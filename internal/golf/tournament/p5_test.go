package tournament

import (
	"testing"

	"github.com/shopspring/decimal"

	"oneclub/internal/golf/experience"
)

func ip(i int) *int { return &i }

func TestTeamHandicaps(t *testing.T) {
	scr := TeamFormatSpec{FormatType: "scramble", TeamSize: 4, Allowances: []int{25, 20, 15, 10}}
	// sorted 4, 8, 12, 20 → 1 + 1.6 + 1.8 + 2 = 6.4 → 6
	if _, team := TeamHandicaps(scr, []*int{ip(20), ip(4), ip(12), ip(8)}); team != 6 {
		t.Fatalf("scramble team handicap %d", team)
	}
	// a missing handicap counts as 0
	if _, team := TeamHandicaps(scr, []*int{ip(20), nil, ip(12), ip(8)}); team != 5 { // 0, 8, 12, 20 → 0+1.6+1.8+2 = 5.4
		t.Fatalf("scramble with a missing handicap %d", team)
	}
	fs := TeamFormatSpec{FormatType: "foursomes", TeamSize: 2, Allowances: []int{50, 50}}
	if _, team := TeamHandicaps(fs, []*int{ip(10), ip(15)}); team != 13 { // 12.5 → 13
		t.Fatalf("foursomes %d", team)
	}
	fb := TeamFormatSpec{FormatType: "four_ball", TeamSize: 2, Allowances: []int{90}}
	if per, _ := TeamHandicaps(fb, []*int{ip(15), ip(7)}); per[0] != 14 || per[1] != 6 { // 13.5 → 14, 6.3 → 6
		t.Fatalf("four-ball %v", per)
	}
	// defaults (WHS) when the format has no allowances
	if _, team := TeamHandicaps(TeamFormatSpec{FormatType: "scramble", TeamSize: 2}, []*int{ip(10), ip(20)}); team != 7 { // 3.5 + 3 = 6.5 → 7
		t.Fatalf("2-player scramble default %d", team)
	}
	if d := DefaultAllowances("texas_scramble", 4); len(d) != 4 || d[0] != 10 {
		t.Fatalf("texas default %v", d)
	}
}

func card(pars []int, strokes []int) experience.Scorecard {
	var s []experience.ScoreHole
	for i, p := range pars {
		si := i + 1
		h := experience.ScoreHole{Seq: i + 1, HoleNumber: i + 1, Par: p, StrokeIndex: &si}
		if i < len(strokes) && strokes[i] > 0 {
			v := strokes[i]
			h.Strokes = &v
		}
		s = append(s, h)
	}
	return experience.Scorecard{Scores: s}
}

func TestTeamRounds(t *testing.T) {
	pars := []int{4, 4, 3, 5}
	a := card(pars, []int{5, 4, 4, 5})
	b := card(pars, []int{4, 6, 3, 6})
	// best ball, no handicap: 4, 4, 3, 5 = 16 (par)
	r := bestBallRound([]experience.Scorecard{a, b}, []int{0, 0}, 1)
	if !r.complete || r.gross != 16 || r.toPar != 0 {
		t.Fatalf("best ball %+v", r)
	}
	// with strokes received on holes 1–2 for player a (playing handicap 2)
	r = bestBallRound([]experience.Scorecard{a, b}, []int{2, 0}, 1)
	if r.net != 4+3+3+5 || r.points != 2+3+2+2 {
		t.Fatalf("best ball net %d points %d", r.net, r.points)
	}
	// best 2 of 2 (sum of both balls) and an unfinished hole
	b2 := card(pars, []int{4, 6, 3})
	r = bestBallRound([]experience.Scorecard{a, b2}, []int{0, 0}, 2)
	if r.complete || r.thru != 3 || r.gross != 9+10+7 {
		t.Fatalf("best two %+v", r)
	}
	// one ball with the team handicap 2 (strokes on SI 1, 2)
	o := oneBallRound(a, 2)
	if o.gross != 18 || o.net != 16 || o.netToPar != 0 {
		t.Fatalf("one ball %+v", o)
	}
	v, score, toPar, _, started := teamMetric([]teamRound{o}, "net")
	if !started || v != 0 || *score != 16 || *toPar != 0 {
		t.Fatalf("metric %d %v %v", v, score, toPar)
	}
	if _, _, _, pts, _ := teamMetric([]teamRound{o}, "stableford"); *pts != o.points {
		t.Fatal("stableford metric")
	}
	// countback: equal totals, better finish wins
	x := oneBallRound(card(pars, []int{5, 5, 3, 5}), 0)
	y := oneBallRound(card(pars, []int{4, 4, 4, 6}), 0)
	if x.gross != y.gross || teamCountback(x, y, "gross") >= 0 {
		t.Fatalf("countback %d vs %d", x.gross, y.gross)
	}
}

func TestFormTeams(t *testing.T) {
	g := formTeams(8, 4, true)
	if len(g) != 2 || len(g[0]) != 4 || g[0][0] != 0 || g[0][1] != 3 || g[0][2] != 4 || g[0][3] != 7 {
		t.Fatalf("snake draft %v", g)
	}
	if g := formTeams(4, 2, false); len(g) != 2 || g[0][1] != 1 || g[1][0] != 2 {
		t.Fatalf("registration order %v", g)
	}
	if g := formTeams(5, 4, true); len(g) != 2 || len(g[0])+len(g[1]) != 5 {
		t.Fatalf("uneven %v", g)
	}
	if formTeams(1, 4, true) != nil {
		t.Fatal("one player: no team")
	}
}

func TestSeriesEventPoints(t *testing.T) {
	table := SeriesPointsSnapshot{Points: []int{10, 6, 4, 2, 1}, ParticipationPoints: 1, TieRule: "split"}
	fs := []seriesFinisher{{Key: "a", Position: ip(1)}, {Key: "b", Position: ip(2)}, {Key: "c", Position: ip(2)}, {Key: "d", Position: ip(4)},
		{Key: "e", Position: ip(9)}, {Key: "f", Label: "MC"}, {Key: "g", Label: "DQ"}}
	p := seriesEventPoints(table, fs)
	want := map[string]string{"a": "10", "b": "5", "c": "5", "d": "2", "e": "1", "f": "1", "g": "0"}
	for k, w := range want {
		if !p[k].Equal(decimal.RequireFromString(w)) {
			t.Fatalf("%s: %s, want %s", k, p[k], w)
		}
	}
	table.TieRule = "full"
	if p := seriesEventPoints(table, fs); !p["b"].Equal(decimal.NewFromInt(6)) || !p["c"].Equal(decimal.NewFromInt(6)) {
		t.Fatalf("full ties: %v", p)
	}
	// a three-way tie for 4th with two table places left: (2 + 1 + 1) / 3
	table.TieRule = "split"
	p = seriesEventPoints(table, []seriesFinisher{{Key: "x", Position: ip(4)}, {Key: "y", Position: ip(4)}, {Key: "z", Position: ip(4)}})
	if !p["x"].Equal(decimal.RequireFromString("1.33")) {
		t.Fatalf("split beyond the table: %v", p)
	}
}

func TestPlayerKey(t *testing.T) {
	if playerKey(nil, "  Budi Santoso ") != "n:budi santoso" {
		t.Fatal("name key")
	}
}
