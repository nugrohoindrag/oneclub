package loyalty

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// PRD P5 §16 #14: Silver (default), Gold ≥ Rp25 jt, Platinum ≥ Rp75 jt per 12 months.
func tiersP5() (TierThreshold, TierThreshold, TierThreshold, []TierThreshold) {
	silver := TierThreshold{ID: uuid.New(), Rank: 1, MinSpend: d("0")}
	gold := TierThreshold{ID: uuid.New(), Rank: 2, MinSpend: d("25000000")}
	plat := TierThreshold{ID: uuid.New(), Rank: 3, MinSpend: d("75000000")}
	return silver, gold, plat, []TierThreshold{silver, plat, gold}
}

func TestQualifyTier(t *testing.T) {
	silver, gold, plat, all := tiersP5()
	for _, c := range []struct {
		spend string
		want  uuid.UUID
	}{{"0", silver.ID}, {"24999999.99", silver.ID}, {"25000000", gold.ID}, {"74999999", gold.ID}, {"75000000", plat.ID}, {"200000000", plat.ID}} {
		if got := QualifyTier(all, 0, d(c.spend)); got == nil || got.ID != c.want {
			t.Errorf("spend %s: got %v", c.spend, got)
		}
	}
	// A points threshold must be met too.
	pts := []TierThreshold{{ID: uuid.New(), Rank: 5, MinPoints: 1000, MinSpend: d("0")}}
	if QualifyTier(pts, 999, d("1")) != nil || QualifyTier(pts, 1000, d("0")) == nil {
		t.Error("points threshold")
	}
}

func TestDecideTier(t *testing.T) {
	silver, gold, plat, _ := tiersP5()
	today := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	graceEnd := today.AddDate(0, 3, 0)
	past := today.AddDate(0, 0, -1)
	cases := []struct {
		name      string
		kind      string
		cur, qual *TierThreshold
		grace     *time.Time
		locked    bool
		months    int
		outcome   string
		to        *uuid.UUID
		graceWant *time.Time
	}{
		{"upgrade at any evaluation", EvalPeriodic, &silver, &gold, nil, false, 3, OutcomeUpgraded, &gold.ID, nil},
		{"upgrade during grace clears it", EvalAnnual, &gold, &plat, &graceEnd, false, 3, OutcomeUpgraded, &plat.ID, nil},
		{"retain", EvalAnnual, &gold, &gold, nil, false, 3, OutcomeRetained, &gold.ID, nil},
		{"requalified in grace", EvalGraceReview, &gold, &gold, &graceEnd, false, 3, OutcomeRetained, &gold.ID, nil},
		{"annual below: grace 3 months", EvalAnnual, &plat, &gold, nil, false, 3, OutcomeGraceStarted, &plat.ID, &graceEnd},
		{"annual below without grace", EvalAnnual, &plat, &silver, nil, false, 0, OutcomeDowngraded, &silver.ID, nil},
		{"periodic never starts grace", EvalPeriodic, &plat, &gold, nil, false, 3, OutcomeRetained, &plat.ID, nil},
		{"in grace", EvalPeriodic, &plat, &gold, &graceEnd, false, 3, OutcomeInGrace, &plat.ID, &graceEnd},
		{"grace ended", EvalGraceReview, &plat, &gold, &past, false, 3, OutcomeDowngraded, &gold.ID, nil},
		{"grace ends today", EvalGraceReview, &plat, &silver, &today, false, 3, OutcomeDowngraded, &silver.ID, nil},
		{"locked", EvalAnnual, &plat, &silver, nil, true, 3, OutcomeLocked, &plat.ID, nil},
		{"no tier yet", EvalAnnual, nil, &silver, nil, false, 3, OutcomeUpgraded, &silver.ID, nil},
	}
	for _, c := range cases {
		got := DecideTier(c.kind, c.cur, c.qual, c.grace, c.locked, today, c.months)
		if got.Outcome != c.outcome {
			t.Errorf("%s: outcome %s, want %s", c.name, got.Outcome, c.outcome)
			continue
		}
		if (got.To == nil) != (c.to == nil) || (got.To != nil && *got.To != *c.to) {
			t.Errorf("%s: to %v, want %v", c.name, got.To, c.to)
		}
		if (got.GraceUntil == nil) != (c.graceWant == nil) || (got.GraceUntil != nil && !got.GraceUntil.Equal(*c.graceWant)) {
			t.Errorf("%s: grace %v, want %v", c.name, got.GraceUntil, c.graceWant)
		}
	}
}

func TestTierProgramAnnualDue(t *testing.T) {
	p := DefaultTierProgram
	if !p.AnnualDue(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) || p.AnnualDue(time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Error("annual evaluation on 1 January")
	}
}

func TestEvaluateRewardRule(t *testing.T) {
	today := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	two, one, three := 2, 1, 3
	yes, no := true, false
	minSpend := d("1000000")
	from, to := today.AddDate(0, 0, 1), today.AddDate(0, 0, -1)
	for _, c := range []struct {
		name string
		c    RewardRuleCheck
		want string
	}{
		{"open rule", RewardRuleCheck{Today: today, TierRank: -1}, ""},
		{"not yet valid", RewardRuleCheck{Today: today, ValidFrom: &from}, ReasonNotValid},
		{"expired", RewardRuleCheck{Today: today, ValidTo: &to}, ReasonNotValid},
		{"tier too low", RewardRuleCheck{Today: today, TierRank: 1, MinTierRank: &two}, ReasonTier},
		{"tier ok", RewardRuleCheck{Today: today, TierRank: 2, MinTierRank: &two}, ""},
		{"no tier", RewardRuleCheck{Today: today, TierRank: -1, MinTierRank: &one}, ReasonTier},
		{"segment", RewardRuleCheck{Today: today, InSegment: &no}, ReasonSegment},
		{"segment ok", RewardRuleCheck{Today: today, InSegment: &yes}, ""},
		{"visits", RewardRuleCheck{Today: today, Visits: 2, MinVisits: &three}, ReasonVisits},
		{"spend", RewardRuleCheck{Today: today, Spend: d("999999"), MinSpend: &minSpend}, ReasonSpend},
		{"limit", RewardRuleCheck{Today: today, Used: 1, MaxPerCustomer: &one, Quantity: 1}, ReasonLimitReached},
		{"limit with quantity", RewardRuleCheck{Today: today, Used: 1, MaxPerCustomer: &two, Quantity: 2}, ReasonLimitReached},
		{"within limit", RewardRuleCheck{Today: today, Used: 1, MaxPerCustomer: &two, Quantity: 1}, ""},
	} {
		if got := EvaluateRewardRule(c.c); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPeriodRange(t *testing.T) {
	today := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct{ typ, in, want, from, to string }{
		{"month", "", "2026-09", "2026-09-01", "2026-09-30"},
		{"quarter", "", "2026-Q3", "2026-07-01", "2026-09-30"},
		{"year", "", "2025", "2025-01-01", "2025-12-31"},
		{"month", "2026-02", "2026-02", "2026-02-01", "2026-02-28"},
		{"quarter", "2026-Q1", "2026-Q1", "2026-01-01", "2026-03-31"},
	} {
		p, f, to, err := PeriodRange(c.typ, c.in, today)
		if err != nil || p != c.want || f.Format("2006-01-02") != c.from || to.Format("2006-01-02") != c.to {
			t.Errorf("%s %q: %s %s %s %v", c.typ, c.in, p, f, to, err)
		}
	}
	if _, _, _, err := PeriodRange("month", "2026-13", today); err == nil {
		t.Error("invalid month accepted")
	}
	if q, _, _, _ := PeriodRange("quarter", "", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)); q != "2025-Q4" {
		t.Errorf("last quarter of the previous year: %s", q)
	}
}

func TestMonthRange(t *testing.T) {
	p, f, to, err := MonthRange("", time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC))
	if err != nil || p != "2026-02" || f.Day() != 1 || to.Day() != 28 {
		t.Errorf("%s %s %s %v", p, f, to, err)
	}
}
