package loyalty

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Qualification rules of the tier master: all / any thresholds, membership
// types, a window per tier.
func TestTierQualifies(t *testing.T) {
	gold := TierThreshold{ID: uuid.New(), Rank: 2, MinPoints: 2000, MinSpend: d("25000000")}
	for _, c := range []struct {
		name  string
		mode  string
		pts   int64
		spend string
		want  bool
	}{
		{"all: both met", QualifyAll, 2000, "25000000", true},
		{"all: spend only", QualifyAll, 1999, "30000000", false},
		{"all: points only", QualifyAll, 5000, "24999999", false},
		{"any: spend only", QualifyAny, 0, "25000000", true},
		{"any: points only", QualifyAny, 2000, "0", true},
		{"any: neither", QualifyAny, 1999, "24999999.99", false},
		{"default mode is all", "", 2000, "1", false},
	} {
		g := gold
		g.QualifyMode = c.mode
		if got := g.Qualifies(TierBasis{Points: c.pts, Spend: d(c.spend)}); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	entry := TierThreshold{ID: uuid.New(), Rank: 1, QualifyMode: QualifyAny}
	if !entry.Qualifies(TierBasis{Spend: decimal.Zero}) {
		t.Error("an entry tier without thresholds qualifies everyone (any)")
	}
	corp := TierThreshold{ID: uuid.New(), Rank: 1, MembershipTypes: []string{"type-a", "type-b"}}
	if corp.Qualifies(TierBasis{MembershipTypes: []string{"type-c"}}) || !corp.Qualifies(TierBasis{MembershipTypes: []string{"type-c", "type-b"}}) {
		t.Error("membership type rule")
	}
}

// Each tier is qualified on the basis of its own window.
func TestQualifyTierBy(t *testing.T) {
	silver := TierThreshold{ID: uuid.New(), Rank: 1}
	gold := TierThreshold{ID: uuid.New(), Rank: 2, MinSpend: d("25000000"), PeriodMonths: 6}
	plat := TierThreshold{ID: uuid.New(), Rank: 3, MinSpend: d("75000000")}
	basis := map[int]TierBasis{12: {Spend: d("80000000")}, 6: {Spend: d("20000000")}}
	by := func(t TierThreshold) TierBasis {
		m := t.PeriodMonths
		if m == 0 {
			m = 12
		}
		return basis[m]
	}
	if q := QualifyTierBy([]TierThreshold{silver, gold, plat}, by); q == nil || q.ID != plat.ID {
		t.Fatalf("12-month spend reaches Platinum: %v", q)
	}
	if q := QualifyTierBy([]TierThreshold{silver, gold}, by); q == nil || q.ID != silver.ID {
		t.Fatalf("Gold needs Rp25 jt in its 6-month window: %v", q)
	}
}

// The tier ladder: a higher tier needs at least as much of every threshold
// and more of one.
func TestTierLadderHarder(t *testing.T) {
	for _, c := range []struct {
		hp     int64
		hs     string
		lp     int64
		ls     string
		harder bool
	}{
		{0, "25000000", 0, "0", true}, {0, "0", 0, "0", false}, {0, "75000000", 0, "25000000", true}, {100, "25000000", 0, "25000000", true},
		{0, "75000000", 100, "25000000", false}, {0, "24000000", 0, "25000000", false},
	} {
		if got := harder(c.hp, d(c.hs), c.lp, d(c.ls)); got != c.harder {
			t.Errorf("harder(%d %s, %d %s) = %v", c.hp, c.hs, c.lp, c.ls, got)
		}
	}
	if !sameValue("minSpend", "25000000.0000", "25000000") || sameValue("minPoints", int64(1), float64(2)) ||
		!sameValue("membershipTypeIds", []any{"b", "a"}, []string{"a", "b"}) {
		t.Error("sameValue")
	}
}
