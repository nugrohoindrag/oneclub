package tournament

// Team format engine (PRD P5 FR-TRN-P5-01, §16 #11): team handicaps from
// the course handicaps of the members and the configurable allowances, and
// the team score of a round — one ball (Scramble, Texas Scramble,
// Foursomes: the team card with the team handicap) or best ball (Four-ball:
// the best N net / stableford scores of the members per hole, each member
// with their own allowance). Pure functions, unit tested.

import (
	"sort"

	"github.com/shopspring/decimal"

	"oneclub/internal/golf/experience"
)

// TeamFormatSpec is a team format as a tournament keeps it (snapshot).
type TeamFormatSpec struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	FormatType    string `json:"formatType" enum:"scramble,four_ball,foursomes,texas_scramble"`
	TeamSize      int    `json:"teamSize"`
	ScoresPerHole int    `json:"scoresPerHole" doc:"Best ball: best N scores of a hole count"`
	Allowances    []int  `json:"allowances" doc:"% of the course handicap, lowest handicap first (best ball: the allowance of every player)"`
	MinDrives     *int   `json:"minDrives"`
}

// OneBall tells whether the team plays one ball (one team card).
func (f TeamFormatSpec) OneBall() bool { return f.FormatType != "four_ball" }

// DefaultAllowances are the WHS recommendations (configurable per format).
func DefaultAllowances(formatType string, size int) []int {
	switch formatType {
	case "four_ball":
		return []int{90}
	case "foursomes":
		return []int{50, 50}
	case "texas_scramble":
		out := make([]int, size)
		for i := range out {
			out[i] = 10
		}
		return out
	}
	switch size {
	case 2:
		return []int{35, 15}
	case 3:
		return []int{30, 20, 10}
	}
	return []int{25, 20, 15, 10}
}

func pctOf(ch int, pct int) int {
	return int(decimal.NewFromInt(int64(ch * pct)).Div(decimal.NewFromInt(100)).Round(0).IntPart())
}

// TeamHandicaps returns the playing handicap of every member (best ball)
// and the team handicap (one ball: sum of the course handicaps, lowest
// first, times the allowances). Members without a handicap count as 0.
func TeamHandicaps(f TeamFormatSpec, course []*int) (perPlayer []int, team int) {
	allow := f.Allowances
	if len(allow) == 0 {
		allow = DefaultAllowances(f.FormatType, max(f.TeamSize, len(course)))
	}
	perPlayer = make([]int, len(course))
	if !f.OneBall() {
		for i, c := range course {
			if c != nil {
				perPlayer[i] = pctOf(*c, allow[0])
			}
		}
		return perPlayer, 0
	}
	chs := make([]int, len(course))
	for i, c := range course {
		if c != nil {
			chs[i] = *c
		}
	}
	sort.Ints(chs)
	sum := decimal.Zero
	for i, c := range chs {
		if i >= len(allow) {
			break
		}
		sum = sum.Add(decimal.NewFromInt(int64(c * allow[i])))
	}
	return perPlayer, int(sum.Div(decimal.NewFromInt(100)).Round(0).IntPart())
}

// teamHoleScore is the team's score on a hole.
type teamHoleScore struct {
	Seq    int
	Number int
	Par    int
	Gross  *int
	Net    *int
	Points *int
	Count  int // pars counted (best ball: N)
}

// teamRound is a team's round (the team card).
type teamRound struct {
	holes    []teamHoleScore
	n        int
	thru     int
	complete bool
	gross    int
	toPar    int
	netToPar int
	points   int
	net      int
}

func (r *teamRound) total() {
	r.n = len(r.holes)
	for _, h := range r.holes {
		if h.Gross == nil {
			continue
		}
		r.thru++
		r.gross += *h.Gross
		r.net += *h.Net
		r.toPar += *h.Gross - h.Par*h.Count
		r.netToPar += *h.Net - h.Par*h.Count
		r.points += *h.Points
	}
	r.complete = r.n > 0 && r.thru == r.n
}

// oneBallRound is the round of a one-ball team: the team card with the
// team handicap.
func oneBallRound(card experience.Scorecard, teamPH int) teamRound {
	c := calcCard(card, &teamPH)
	r := teamRound{}
	for _, h := range c.holes {
		th := teamHoleScore{Seq: h.Seq, Number: h.Number, Par: h.Par, Count: 1}
		if h.Strokes != nil {
			th.Gross = ptr(*h.Strokes)
			th.Net = ptr(*h.Strokes - h.Received)
			th.Points = ptr(max(0, 2+h.Par+h.Received-*h.Strokes))
		}
		r.holes = append(r.holes, th)
	}
	r.total()
	return r
}

// bestBallRound is the round of a best-ball team: per hole the best N net
// (and gross, stableford) scores of the members, each with their playing
// handicap; a hole counts once N members have a score.
func bestBallRound(cards []experience.Scorecard, phs []int, best int) teamRound {
	r := teamRound{}
	if len(cards) == 0 {
		return r
	}
	best = max(best, 1)
	recs := make([]map[int]int, len(cards))
	for i, c := range cards {
		recs[i] = received(c.Scores, phs[i])
	}
	ref := cards[0].Scores
	for _, c := range cards {
		if len(c.Scores) > len(ref) {
			ref = c.Scores
		}
	}
	for _, h := range ref {
		th := teamHoleScore{Seq: h.Seq, Number: h.HoleNumber, Par: h.Par, Count: best}
		var gross, net, pts []int
		for i, c := range cards {
			for _, x := range c.Scores {
				if x.Seq != h.Seq || x.Strokes == nil {
					continue
				}
				rec := recs[i][x.Seq]
				gross = append(gross, *x.Strokes)
				net = append(net, *x.Strokes-rec)
				pts = append(pts, max(0, 2+x.Par+rec-*x.Strokes))
			}
		}
		if len(gross) >= best {
			sort.Ints(gross)
			sort.Ints(net)
			sort.Sort(sort.Reverse(sort.IntSlice(pts)))
			g, n, p := 0, 0, 0
			for k := 0; k < best; k++ {
				g, n, p = g+gross[k], n+net[k], p+pts[k]
			}
			th.Gross, th.Net, th.Points = &g, &n, &p
		}
		r.holes = append(r.holes, th)
	}
	r.total()
	return r
}

// value is the ranking value of a team round set in a category (lower is
// better; stableford negated) and whether it is meaningful.
func teamMetric(rounds []teamRound, cat string) (value int, score *int, toPar *int, points *int, started bool) {
	v, tot, totOK := 0, 0, true
	for _, r := range rounds {
		if r.thru > 0 {
			started = true
		}
		switch cat {
		case "stableford":
			v += r.points
			tot += r.points
		case "net":
			v += r.netToPar
			if r.complete {
				tot += r.net
			} else if r.thru > 0 {
				totOK = false
			}
		default:
			v += r.toPar
			if r.complete {
				tot += r.gross
			} else if r.thru > 0 {
				totOK = false
			}
		}
	}
	if cat == "stableford" {
		points = ptr(v)
		v = -v
	} else {
		toPar = ptr(v)
	}
	if totOK && started {
		score = ptr(tot)
	}
	return v, score, toPar, points, started
}

// teamSegment is the countback value of the last k holes of a team round.
func teamSegment(r teamRound, cat string, k int) int {
	s := 0
	for i := r.n - k; i < r.n; i++ {
		if i < 0 {
			continue
		}
		h := r.holes[i]
		if h.Gross == nil {
			continue
		}
		switch cat {
		case "stableford":
			s -= *h.Points
		case "net":
			s += *h.Net
		default:
			s += *h.Gross
		}
	}
	return s
}

// teamCountback compares the last rounds of two teams on the last 9, 6, 3
// and 1 holes (<0: a is better).
func teamCountback(a, b teamRound, cat string) int {
	if a.n == 0 || a.n != b.n || !a.complete || !b.complete {
		return 0
	}
	for _, k := range []int{a.n / 2, a.n / 3, a.n / 6, 1} {
		if k <= 0 {
			continue
		}
		if c := teamSegment(a, cat, k) - teamSegment(b, cat, k); c != 0 {
			return c
		}
	}
	return 0
}
