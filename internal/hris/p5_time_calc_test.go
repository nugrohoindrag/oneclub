package hris

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var jkt = time.FixedZone("WIB", 7*3600)

func at(h, m int) time.Time { return time.Date(2026, 10, 5, h, m, 0, 0, jkt) }

func shift(sh, sm, eh, em, brk int) *ShiftWindow {
	end := at(eh, em)
	if !end.After(at(sh, sm)) {
		end = end.AddDate(0, 0, 1)
	}
	return &ShiftWindow{Start: at(sh, sm), End: end, BreakMinutes: brk}
}

func ev(dir string, t time.Time) ClockEvent { return ClockEvent{Direction: dir, At: t} }

func TestEvaluateDay(t *testing.T) {
	pol := NewAttendancePolicy() // tolerance 10, absent after 240
	cases := []struct {
		name               string
		in                 DayInput
		status             string
		late, early, ot, w int
		flags              []string
		final              bool
	}{
		{name: "on time", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Events: []ClockEvent{ev("in", at(7, 55)), ev("out", at(17, 2))}, Now: at(23, 0)},
			status: DayPresent, ot: 7, w: 487, final: true},
		{name: "late within tolerance", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Events: []ClockEvent{ev("in", at(8, 9)), ev("out", at(17, 0))},
			Now: at(23, 0)}, status: DayPresent, w: 471, final: true},
		{name: "late", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Events: []ClockEvent{ev("in", at(8, 25)), ev("out", at(17, 0))}, Now: at(23, 0)},
			status: DayLate, late: 25, w: 455, final: true},
		{name: "late excused by permission", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Events: []ClockEvent{ev("in", at(9, 30)), ev("out", at(17, 0))},
			Permissions: []PermissionWindow{{Type: "LATE_ARRIVAL", Start: at(8, 0), End: at(9, 30), Paid: true}}, Now: at(23, 0)},
			status: DayPresent, w: 390, final: true},
		{name: "early leave", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Events: []ClockEvent{ev("in", at(8, 0)), ev("out", at(15, 0))}, Now: at(23, 0)},
			status: DayEarlyLeave, early: 120, w: 360, final: true},
		{name: "still before absence threshold", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Now: at(9, 0)}, status: DayScheduled},
		{name: "absent after threshold", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Now: at(12, 30)}, status: DayAbsent},
		{name: "absent finalized", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Finalize: true, Now: at(9, 0)}, status: DayAbsent, final: true},
		{name: "missing clock-out", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Events: []ClockEvent{ev("in", at(8, 0))}, Now: at(23, 30)},
			status: DayPresent, flags: []string{FlagMissingOut}, final: true},
		{name: "night shift across midnight", in: DayInput{Shift: shift(22, 0, 6, 0, 60),
			Events: []ClockEvent{ev("in", at(21, 58)), ev("out", at(6, 31).AddDate(0, 0, 1))}, Now: at(12, 0).AddDate(0, 0, 1)},
			status: DayPresent, ot: 33, w: 453, final: true},
		{name: "full day leave", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Leave: &DayLeave{Type: "ANNUAL", Paid: true, Fraction: decimal.NewFromInt(1)},
			Now: at(23, 0)}, status: DayOnLeave, final: true},
		{name: "half day leave am", in: DayInput{Shift: shift(8, 0, 17, 0, 60), Leave: &DayLeave{Type: "ANNUAL", Paid: true, Fraction: decimal.RequireFromString("0.5"),
			HalfDay: "am"}, Events: []ClockEvent{ev("in", at(12, 30)), ev("out", at(17, 0))}, Now: at(23, 0)}, status: DayPresent, w: 270, final: true},
		{name: "day off", in: DayInput{Off: true, Now: at(23, 0)}, status: DayOff, final: true},
		{name: "holiday", in: DayInput{Holiday: "Independence Day", Now: at(23, 0)}, status: DayHoliday, final: true},
		{name: "rest day work", in: DayInput{Off: true, Events: []ClockEvent{ev("in", at(9, 0)), ev("out", at(12, 0))}, Now: at(23, 0)},
			status: DayPresent, ot: 180, w: 180, flags: []string{FlagNoSchedule}, final: true},
		{name: "holiday shift is all overtime", in: DayInput{Shift: shift(8, 0, 12, 0, 0), Holiday: "Eid", Events: []ClockEvent{ev("in", at(8, 0)), ev("out", at(12, 0))},
			Now: at(23, 0)}, status: DayPresent, ot: 240, w: 240, final: true},
	}
	for _, c := range cases {
		c.in.Policy = pol
		r := EvaluateDay(c.in)
		if r.Status != c.status || r.LateMinutes != c.late || r.EarlyLeaveMinutes != c.early || r.OvertimeMinutes != c.ot || r.WorkedMinutes != c.w ||
			r.Final != c.final {
			t.Errorf("%s: got %+v", c.name, r)
		}
		for _, f := range c.flags {
			found := false
			for _, g := range r.Flags {
				found = found || g == f
			}
			if !found {
				t.Errorf("%s: flag %s missing in %v", c.name, f, r.Flags)
			}
		}
	}
	// unpaid permission minutes
	r := EvaluateDay(DayInput{Shift: shift(8, 0, 17, 0, 60), Events: []ClockEvent{ev("in", at(8, 0)), ev("out", at(17, 0))}, Policy: pol, Now: at(23, 0),
		Permissions: []PermissionWindow{{Type: "PERSONAL", Start: at(13, 0), End: at(15, 0)}}})
	if r.UnpaidPermissionMinutes != 120 {
		t.Errorf("unpaid permission: %+v", r)
	}
}

// EP-08 AC: Rp5,190,000 ÷ 173 = Rp30,000 per hour; 3 hours on a workday
// at 1.5× the first hour and 2× the next = Rp45,000 + Rp120,000.
func TestOvertimeTiers(t *testing.T) {
	p := NewOvertimePolicy()
	tiers := p.Tiers(decimal.NewFromInt(3), OvertimeWorkday, 5)
	if len(tiers) != 2 || tiers[0].Factor != "1.5" || !tiers[0].Hours.Equal(decimal.NewFromInt(1)) || tiers[1].Factor != "2" ||
		!tiers[1].Hours.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("tiers: %+v", tiers)
	}
	mult := MultipliedOf(tiers)
	if !mult.Equal(decimal.RequireFromString("5.5")) {
		t.Fatalf("multiplied: %s", mult)
	}
	pay := p.HourlyWage(decimal.NewFromInt(5190000)).Mul(mult)
	if !pay.Equal(decimal.NewFromInt(165000)) {
		t.Fatalf("pay: %s", pay)
	}
	// rest day, 5-day week: 10 hours = 8 × 2 + 1 × 3 + 1 × 4
	rest := p.Tiers(decimal.NewFromInt(10), OvertimeRestDay, 5)
	if len(rest) != 3 || !MultipliedOf(rest).Equal(decimal.NewFromInt(23)) {
		t.Fatalf("rest day: %+v", rest)
	}
	// shortest day of a 6-day week: 6 h = 5 × 2 + 1 × 3
	if s := p.Tiers(decimal.NewFromInt(6), OvertimeShortestDay, 6); !MultipliedOf(s).Equal(decimal.NewFromInt(13)) {
		t.Fatalf("shortest day: %+v", s)
	}
	// half hours
	if h := p.Tiers(decimal.RequireFromString("1.5"), OvertimeWorkday, 5); !MultipliedOf(h).Equal(decimal.RequireFromString("2.5")) {
		t.Fatalf("1.5 h: %+v", h)
	}
}

func TestPayableOvertime(t *testing.T) {
	p := NewOvertimePolicy() // minimum 30, rounding 30, max 4 / day
	for _, c := range []struct {
		approved string
		actual   int
		want     string
	}{
		{"2", 140, "2"}, {"3", 100, "1.5"}, {"2", 20, "0"}, {"6", 400, "4"}, {"1", 0, "0"},
	} {
		if got := p.PayableHours(decimal.RequireFromString(c.approved), c.actual); !got.Equal(decimal.RequireFromString(c.want)) {
			t.Errorf("approved %s actual %d: got %s want %s", c.approved, c.actual, got, c.want)
		}
	}
}

func TestDayKind(t *testing.T) {
	if DayKindOf(false, false, 5, time.Monday) != OvertimeWorkday || DayKindOf(true, false, 5, time.Monday) != OvertimeRestDay ||
		DayKindOf(false, true, 5, time.Saturday) != OvertimeRestDay || DayKindOf(true, false, 6, time.Saturday) != OvertimeShortestDay {
		t.Fatal("day kinds")
	}
	if IsWorkingWeekday(time.Saturday, 5) || !IsWorkingWeekday(time.Saturday, 6) || IsWorkingWeekday(time.Sunday, 6) || !IsWorkingWeekday(time.Friday, 5) {
		t.Fatal("working weekdays")
	}
}

// §16 #3: 12 days after 12 months; then every 1 January; carry-over at
// most 6 days lapsing on 31 March.
func TestLeaveAccrual(t *testing.T) {
	lp := NewLeavePolicy()
	annual, _ := lp.LeaveType("ANNUAL")
	join := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	if _, ok := AnnualGrant(annual, join, 2025); ok {
		t.Fatal("not eligible in the first year")
	}
	g, ok := AnnualGrant(annual, join, 2026)
	if !ok || g.Format("2006-01-02") != "2026-06-15" {
		t.Fatalf("first grant: %v %v", g, ok)
	}
	g, ok = AnnualGrant(annual, join, 2027)
	if !ok || g.Format("2006-01-02") != "2027-01-01" {
		t.Fatalf("second grant: %v %v", g, ok)
	}
	if g, ok := AnnualGrant(annual, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), 2026); !ok || g.Format("01-02") != "01-01" {
		t.Fatalf("eligible on 1 January: %v", g)
	}
	sick, _ := lp.LeaveType("SICK")
	if _, ok := AnnualGrant(sick, join, 2027); ok || sick.HasBalance() || !annual.HasBalance() {
		t.Fatal("sick leave has no balance")
	}
	if c := lp.CarryOver(decimal.NewFromInt(9)); !c.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("carry-over cap: %s", c)
	}
	if c := lp.CarryOver(decimal.NewFromInt(-1)); !c.IsZero() {
		t.Fatalf("negative carry-over: %s", c)
	}
	if lp.CarryOverExpiryDate(2027).Format("2006-01-02") != "2027-03-31" {
		t.Fatal("carry-over expiry")
	}
	if AddMonths(time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC), 1).Format("2006-01-02") != "2025-02-28" {
		t.Fatal("add months clamps")
	}
}

// EP-07 AC: a clock-in 400 m from the geofence centre is outside a 150 m
// (or the §16 #7 300 m) radius.
func TestDistance(t *testing.T) {
	lat, lng := -6.2297, 106.6894
	d := DistanceMeters(lat, lng, lat+400.0/111320.0, lng)
	if d < 395 || d > 405 {
		t.Fatalf("distance: %f", d)
	}
}

func TestAttendanceFactor(t *testing.T) {
	s := TimeSummary{ScheduledDays: 20, PresentDays: 18, PaidLeaveDays: decimal.NewFromInt(1)}
	if !s.AttendanceFactor(false).Equal(decimal.RequireFromString("0.9")) || !s.AttendanceFactor(true).Equal(decimal.RequireFromString("0.95")) {
		t.Fatal("factor")
	}
	if !(TimeSummary{}).AttendanceFactor(false).Equal(decimal.NewFromInt(1)) {
		t.Fatal("nothing scheduled")
	}
}
