package membership

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/crm"
)

func ip(n int) *int { return &n }

func born(day time.Time, years, months int) *time.Time {
	t := day.AddDate(-years, -months, 0)
	return &t
}

// PRD P1 EP-06 AC: a Family membership with a 22-year-old child fails the
// max child age rule; a 10-year-old passes.
func TestEvaluateFamily(t *testing.T) {
	day := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	rules := Eligibility{MinAge: ip(18), MaxChildAge: ip(21), MaxChildren: ip(3)}
	applicant := crm.Customer{Name: "Principal", BirthDate: born(day, 46, 0)}
	child22, child10, spouse := uuid.New(), uuid.New(), uuid.New()
	profiles := map[uuid.UUID]crm.Customer{
		child22: {Name: "Older child", BirthDate: born(day, 22, 1)},
		child10: {Name: "Young child", BirthDate: born(day, 10, 0)},
		spouse:  {Name: "Spouse", BirthDate: born(day, 44, 0)},
	}
	res := Evaluate(rules, applicant, []Dependent{{CustomerID: spouse, Relationship: "spouse"}, {CustomerID: child22, Relationship: "child"}}, profiles, false, false, day)
	if res.Eligible {
		t.Fatalf("22-year-old child must fail: %+v", res)
	}
	res = Evaluate(rules, applicant, []Dependent{{CustomerID: spouse, Relationship: "spouse"}, {CustomerID: child10, Relationship: "child"}}, profiles, false, false, day)
	if !res.Eligible {
		t.Fatalf("10-year-old child must pass: %+v", res)
	}
}

func TestEvaluateApplicantRules(t *testing.T) {
	day := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		rules Eligibility
		c     crm.Customer
		want  bool
	}{
		{"junior too old", Eligibility{MaxAge: ip(18)}, crm.Customer{BirthDate: born(day, 19, 0)}, false},
		{"junior ok", Eligibility{MaxAge: ip(18)}, crm.Customer{BirthDate: born(day, 17, 0)}, true},
		{"unknown birth date fails max age", Eligibility{MaxAge: ip(18)}, crm.Customer{}, false},
		{"ladies", Eligibility{Gender: "female"}, crm.Customer{Gender: "male"}, false},
		{"resident from profile", Eligibility{ResidentOnly: true}, crm.Customer{Resident: true}, true},
		{"resident missing", Eligibility{ResidentOnly: true}, crm.Customer{}, false},
		{"no rules", Eligibility{}, crm.Customer{}, true},
	}
	for _, tc := range cases {
		if got := Evaluate(tc.rules, tc.c, nil, nil, false, false, day).Eligible; got != tc.want {
			t.Errorf("%s: eligible=%v, want %v", tc.name, got, tc.want)
		}
	}
	if !Evaluate(Eligibility{ResidentOnly: true}, crm.Customer{}, nil, nil, false, true, day).Eligible {
		t.Error("a verified resident proof satisfies resident-only")
	}
}

func TestPeriodEnd(t *testing.T) {
	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	if got := PeriodEnd(start, "year", 1); !got.Equal(time.Date(2027, 1, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("1 year: %s", got)
	}
	if got := PeriodEnd(start, "month", 6); !got.Equal(time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("6 months: %s", got)
	}
}
