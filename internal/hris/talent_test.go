package hris

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dp(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}

func TestWeightedScore(t *testing.T) {
	cases := []struct {
		name     string
		items    []ScoreItem
		want     string
		complete bool
	}{
		{"weighted", []ScoreItem{{decimal.NewFromInt(3), dp("4")}, {decimal.NewFromInt(1), dp("2")}}, "3.5", true},
		{"equal weights", []ScoreItem{{decimal.NewFromInt(1), dp("5")}, {decimal.NewFromInt(1), dp("4")}, {decimal.NewFromInt(1), dp("4")}}, "4.33", true},
		{"zero weight counts as 1", []ScoreItem{{decimal.Zero, dp("2")}, {decimal.NewFromInt(1), dp("4")}}, "3", true},
		{"missing score", []ScoreItem{{decimal.NewFromInt(2), dp("4")}, {decimal.NewFromInt(1), nil}}, "4", false},
		{"nothing scored", []ScoreItem{{decimal.NewFromInt(1), nil}}, "0", false},
		{"empty", nil, "0", false},
		{"rounding", []ScoreItem{{decimal.NewFromInt(3), dp("3.75")}, {decimal.NewFromInt(4), dp("4.1")}}, "3.95", true},
	}
	for _, c := range cases {
		got, complete := WeightedScore(c.items)
		if !got.Equal(decimal.RequireFromString(c.want)) || complete != c.complete {
			t.Errorf("%s: got %s/%v, want %s/%v", c.name, got, complete, c.want, c.complete)
		}
	}
}

func TestCombineScores(t *testing.T) {
	cases := []struct {
		comp, kpi *decimal.Decimal
		weight    string
		want      string
	}{
		{dp("4"), dp("3"), "60", "3.6"},
		{dp("4"), dp("3"), "100", "4"},
		{dp("4"), dp("3"), "0", "3"},
		{dp("4"), nil, "60", "4"},
		{nil, dp("2.5"), "60", "2.5"},
		{dp("4"), dp("3"), "150", "4"}, // clamped to 100
		{dp("3.33"), dp("4.67"), "50", "4"},
	}
	for _, c := range cases {
		got := CombineScores(c.comp, c.kpi, decimal.RequireFromString(c.weight))
		if got == nil || !got.Equal(decimal.RequireFromString(c.want)) {
			t.Errorf("CombineScores(%v, %v, %s) = %v, want %s", c.comp, c.kpi, c.weight, got, c.want)
		}
	}
	if CombineScores(nil, nil, decimal.NewFromInt(50)) != nil {
		t.Error("no part should give nil")
	}
}

func TestRecommendedScore(t *testing.T) {
	cfg := NewPerformanceConfiguration()
	if got := cfg.RecommendedScore(dp("4"), dp("5")); !got.Equal(decimal.NewFromInt(4)) {
		t.Errorf("default (self weight 0): %s", got)
	}
	cfg.SelfWeightPercent = 20
	if got := cfg.RecommendedScore(dp("4"), dp("5")); !got.Equal(decimal.RequireFromString("4.2")) {
		t.Errorf("self weight 20: %s", got)
	}
	if got := cfg.RecommendedScore(dp("4"), nil); !got.Equal(decimal.NewFromInt(4)) {
		t.Errorf("no self score: %s", got)
	}
	if cfg.RecommendedScore(nil, dp("4")) != nil {
		t.Error("no manager score should give nil")
	}
}

func TestBands(t *testing.T) {
	cfg := NewPerformanceConfiguration()
	cases := map[string]string{"5": "outstanding", "4.5": "outstanding", "4.49": "exceeds", "3.75": "exceeds", "3.74": "meets", "3": "meets",
		"2.99": "needs_improvement", "2": "needs_improvement", "1.99": "unsatisfactory", "1": "unsatisfactory"}
	for score, want := range cases {
		if got := cfg.Band(decimal.RequireFromString(score)).Code; got != want {
			t.Errorf("Band(%s) = %s, want %s", score, got, want)
		}
	}
	if !cfg.AtLeast("exceeds", "meets") || cfg.AtLeast("meets", "exceeds") || !cfg.AtLeast("meets", "meets") {
		t.Error("AtLeast ordering")
	}
	if !cfg.AtLeast("meets", "unknown") || cfg.AtLeast("unknown", "meets") {
		t.Error("AtLeast unknown codes")
	}
	// Bands are ordered by minimum score whatever the configured order.
	cfg.RatingBands = []RatingBand{{Code: "low", MinScore: "0"}, {Code: "high", MinScore: "4"}, {Code: "mid", MinScore: "2.5"}}
	for score, want := range map[string]string{"4.2": "high", "3": "mid", "1": "low"} {
		if got := cfg.Band(decimal.RequireFromString(score)).Code; got != want {
			t.Errorf("unordered Band(%s) = %s, want %s", score, got, want)
		}
	}
	if (PerformanceConfiguration{}).Band(decimal.NewFromInt(3)).Code != "" {
		t.Error("no bands should give the zero band")
	}
}

func TestDistribution(t *testing.T) {
	cfg := NewPerformanceConfiguration()
	ratings := []string{"outstanding", "outstanding", "exceeds", "meets", "meets", "meets", "meets", "meets", "needs_improvement", "meets"}
	d := cfg.Distribution(ratings)
	if len(d) != 5 || d[0].Code != "outstanding" || d[0].Count != 2 || d[0].SharePercent != "20" || !d[0].Over {
		t.Fatalf("outstanding: %+v", d)
	}
	if d[1].Code != "exceeds" || d[1].Over || d[1].SharePercent != "10" {
		t.Errorf("exceeds: %+v", d[1])
	}
	if d[2].Count != 6 || d[2].Over || d[2].MaxSharePercent != 0 {
		t.Errorf("meets has no limit: %+v", d[2])
	}
	if e := cfg.Distribution(nil); len(e) != 5 || e[0].SharePercent != "0" || e[0].Over {
		t.Errorf("empty: %+v", e)
	}
}

func TestValidScore(t *testing.T) {
	for s, ok := range map[string]bool{"1": true, "5": true, "3.25": true, "0.99": false, "5.01": false, "3.333": false, "0": false} {
		if ValidScore(decimal.RequireFromString(s), 5) != ok {
			t.Errorf("ValidScore(%s) != %v", s, ok)
		}
	}
}

func TestStageRank(t *testing.T) {
	if StageRank(StageApplied) >= StageRank(StageScreening) || StageRank(StageInterview) >= StageRank(StageOffered) ||
		StageRank(StageOffered) >= StageRank(StageHired) {
		t.Error("pipeline order")
	}
	if StageRank(StageRejected) != -1 || StageRank(StageWithdrawn) != -1 {
		t.Error("closed stages rank -1")
	}
}
