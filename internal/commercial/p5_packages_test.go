package commercial

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func p5comp(seq int, name string, start *string, dur int) ComponentSpec {
	return ComponentSpec{ID: uuid.New(), Seq: seq, Name: name, ComponentType: "service", Quantity: "1", StartTime: start, DurationMinutes: &dur}
}

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

func TestResolveChoices(t *testing.T) {
	golf := p5comp(1, "Golf", strp("07:00"), 240)
	spa := p5comp(2, "Spa", nil, 60)
	tennis := p5comp(3, "Tennis", nil, 60)
	comps := []ComponentSpec{golf, spa, tennis}
	g := "WELLNESS"
	rules := map[uuid.UUID]componentRule{spa.ID: {ComponentID: spa.ID, ChoiceGroup: &g, ChoicePick: 1}, tennis.ID: {ComponentID: tennis.ID, ChoiceGroup: &g,
		ChoicePick: 1}}
	cases := []struct {
		name   string
		chosen []uuid.UUID
		want   []string
		err    bool
	}{
		{"default first alternative", nil, []string{"Golf", "Spa"}, false},
		{"tennis chosen", []uuid.UUID{tennis.ID}, []string{"Golf", "Tennis"}, false},
		{"both chosen", []uuid.UUID{spa.ID, tennis.ID}, nil, true},
	}
	for _, c := range cases {
		out, used, err := resolveChoices(comps, rules, c.chosen, true)
		if (err != nil) != c.err {
			t.Fatalf("%s: err %v", c.name, err)
		}
		if c.err {
			continue
		}
		var names []string
		for _, x := range out {
			names = append(names, x.Name)
			if x.Optional {
				t.Fatalf("%s: a chosen alternative is part of the package", c.name)
			}
		}
		if len(names) != len(c.want) || names[0] != c.want[0] || names[1] != c.want[1] {
			t.Fatalf("%s: %v, want %v", c.name, names, c.want)
		}
		if len(used) != len(c.chosen) {
			t.Fatalf("%s: used %v", c.name, used)
		}
	}
	if _, _, err := resolveChoices(comps, rules, nil, false); err == nil {
		t.Fatal("without auto, a choice is required")
	}
	if out, _, _ := resolveChoices(comps, map[uuid.UUID]componentRule{}, nil, false); len(out) != 3 {
		t.Fatal("no rules: every component")
	}
}

func TestPlanSchedule(t *testing.T) {
	loc := time.FixedZone("WIB", 7*3600)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, loc)
	golf := p5comp(1, "Golf", strp("07:00"), 240)
	spa := p5comp(2, "Spa", nil, 60)
	dinner := p5comp(3, "Dinner", strp("18:30"), 90)
	at := func(s planSlot) string { return s.Start.In(loc).Format("15:04") }

	// spa 30 minutes after golf
	rules := map[uuid.UUID]componentRule{spa.ID: {ComponentID: spa.ID, AfterComponentID: &golf.ID, MinGap: 30, MaxGap: intp(120)}}
	p, err := planSchedule([]ComponentSpec{spa, golf, dinner}, rules, day, "07:00", loc, nil)
	if err != nil || at(p[spa.ID]) != "11:30" || at(p[golf.ID]) != "07:00" || at(p[dinner.ID]) != "18:30" {
		t.Fatalf("sequence: %v %v", p, err)
	}
	// service window moves a free component
	ws, we := "12:00", "18:00"
	rules[spa.ID] = componentRule{ComponentID: spa.ID, AfterComponentID: &golf.ID, MinGap: 30, MaxGap: intp(120), WindowStart: &ws, WindowEnd: &we}
	if p, err = planSchedule([]ComponentSpec{golf, spa}, rules, day, "07:00", loc, nil); err != nil || at(p[spa.ID]) != "12:00" {
		t.Fatalf("window: %v %v", p, err)
	}
	// the fit (time block) moves beyond the maximum gap: refused
	late := func(c ComponentSpec, s planSlot) (time.Time, error) { return s.Start.Add(3 * time.Hour), nil }
	if _, err = planSchedule([]ComponentSpec{golf, spa}, rules, day, "07:00", loc, late); err == nil {
		t.Fatal("maximum gap exceeded")
	}
	// a fixed start before the end of the previous component is refused
	fixed := p5comp(2, "Spa", strp("10:00"), 60)
	if _, err = planSchedule([]ComponentSpec{golf, fixed}, map[uuid.UUID]componentRule{fixed.ID: {ComponentID: fixed.ID, AfterComponentID: &golf.ID}}, day, "07:00",
		loc, nil); err == nil {
		t.Fatal("fixed start before the previous component ends")
	}
	// a component ending after its window is refused
	we2 := "10:30"
	if _, err = planSchedule([]ComponentSpec{golf}, map[uuid.UUID]componentRule{golf.ID: {ComponentID: golf.ID, WindowEnd: &we2}}, day, "07:00", loc,
		nil); err == nil {
		t.Fatal("window end")
	}
	// cycles are refused
	a, b := p5comp(1, "A", nil, 60), p5comp(2, "B", nil, 60)
	cyc := map[uuid.UUID]componentRule{a.ID: {ComponentID: a.ID, AfterComponentID: &b.ID}, b.ID: {ComponentID: b.ID, AfterComponentID: &a.ID}}
	if _, err = planSchedule([]ComponentSpec{a, b}, cyc, day, "07:00", loc, nil); err == nil {
		t.Fatal("cycle")
	}
}

func TestBlockOf(t *testing.T) {
	ws := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	f, to := blockOf(ws.Add(95*time.Minute), ws, 60)
	if f.Format("15:04") != "11:00" || to.Format("15:04") != "12:00" {
		t.Fatalf("block %s–%s", f, to)
	}
	if f, _ := blockOf(ws.Add(-time.Hour), ws, 30); !f.Equal(ws) {
		t.Fatal("before the window: first block")
	}
}

func TestRuleCost(t *testing.T) {
	d := decimal.RequireFromString
	for _, c := range []struct {
		basis, amount, qty, revenue string
		pax                         int
		want                        string
	}{
		{"per_unit", "150000", "4", "0", 1, "600000"},
		{"per_booking", "50000", "9", "0", 3, "50000"},
		{"per_pax", "25000", "1", "0", 3, "75000"},
		{"percent_of_revenue", "10", "1", "1234567", 1, "123456.7"},
	} {
		if got := ruleCost(c.basis, d(c.amount), d(c.qty), d(c.revenue), c.pax); !got.Equal(d(c.want)) {
			t.Fatalf("%s: %s, want %s", c.basis, got, c.want)
		}
	}
}

func TestScheduleFromTemplate(t *testing.T) {
	today := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	lines := []PaymentTemplateLine{{Label: "Deposit", Kind: "down_payment", Percent: "40", Days: 0, From: "booking"},
		{Label: "Second", Kind: "installment", Percent: "30", Days: 30, From: "before_start"}, {Label: "Balance", Kind: "final", Percent: "30", Days: 3,
			From: "before_start"}}
	out := scheduleFromTemplate(lines, "WED", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), today)
	if len(out) != 3 || out[0].DueDate != "2026-10-05" || out[0].Percent != "40" || out[1].DueDate != "2026-10-05" || out[2].DueDate != "2026-10-17" ||
		out[2].Percent != "" || out[0].Label != "Deposit (WED)" {
		t.Fatalf("schedule: %+v", out)
	}
}

func TestCapacityApplies(t *testing.T) {
	to := "2026-12-31"
	c := capacityRule{DateFrom: "2026-11-01", DateTo: &to, Weekdays: []int32{6, 7}}
	sat := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	if !c.applies(sat) || c.applies(sat.AddDate(0, 0, 2)) || c.applies(time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("capacity dates and weekdays")
	}
	if !basisUsed("pax", decimal.Zero, 2, 7).Equal(decimal.NewFromInt(7)) || !basisNeed("units", decimal.NewFromInt(3), 1).Equal(decimal.NewFromInt(3)) {
		t.Fatal("capacity basis")
	}
}
