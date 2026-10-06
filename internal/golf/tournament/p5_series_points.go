package tournament

// Order of Merit points engine (PRD P5 FR-TRN-P5-02): points per position
// of a configurable points table (versioned: a series keeps the snapshot
// taken at activation), ties split the points of the tied positions or
// all receive the full points, finishers beyond the table and players who
// missed the cut get the participation points, DQ / WD / NR none; the
// event weight multiplies the points (final event). Pure, unit tested.

import (
	"github.com/shopspring/decimal"
)

// SeriesPointsSnapshot is the points table a series uses.
type SeriesPointsSnapshot struct {
	Code                string `json:"code"`
	Name                string `json:"name"`
	Points              []int  `json:"points" doc:"Points of positions 1, 2, 3 …"`
	ParticipationPoints int    `json:"participationPoints"`
	TieRule             string `json:"tieRule" enum:"split,full"`
}

// seriesFinisher is a player's result of an event.
type seriesFinisher struct {
	Key      string
	Position *int
	Label    string // MC, DQ, WD, NR, - or the position label
}

func (t SeriesPointsSnapshot) at(pos int) decimal.Decimal {
	if pos >= 1 && pos <= len(t.Points) {
		return decimal.NewFromInt(int64(t.Points[pos-1]))
	}
	return decimal.NewFromInt(int64(t.ParticipationPoints))
}

// seriesEventPoints returns the base points (before the event weight) of
// every finisher of an event.
func seriesEventPoints(t SeriesPointsSnapshot, fs []seriesFinisher) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	tied := map[int]int{}
	for _, f := range fs {
		if f.Position != nil {
			tied[*f.Position]++
		}
	}
	for _, f := range fs {
		switch {
		case f.Position != nil:
			p, k := *f.Position, tied[*f.Position]
			if k <= 1 || t.TieRule == "full" {
				out[f.Key] = t.at(p)
				continue
			}
			sum := decimal.Zero
			for i := 0; i < k; i++ {
				sum = sum.Add(t.at(p + i))
			}
			out[f.Key] = sum.Div(decimal.NewFromInt(int64(k))).Round(2)
		case f.Label == "MC":
			out[f.Key] = decimal.NewFromInt(int64(t.ParticipationPoints))
		default:
			out[f.Key] = decimal.Zero
		}
	}
	return out
}
