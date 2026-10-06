package hris

// Calculation engine of time & attendance (pure functions, unit tested):
// matching clock events to the shift of a day (FR-ATT-03), the day kind of
// overtime (FR-OVT-03: workday, rest day / holiday, shortest day of a
// 6-day week), payable overtime and its multiplier tiers (FR-OVT-02/03),
// annual leave accrual and carry-over (§16 #3) and the geofence distance
// (FR-ATT-05).

import (
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ShiftWindow is the scheduled shift of a day.
type ShiftWindow struct {
	Start, End   time.Time
	BreakMinutes int
}

// ClockEvent is one accepted clock-in / out.
type ClockEvent struct {
	Direction string // in | out
	At        time.Time
}

// DayLeave is approved leave on a day: Fraction 1 (full day) or 0.5 with
// HalfDay am / pm.
type DayLeave struct {
	Type     string
	Paid     bool
	Fraction decimal.Decimal
	HalfDay  string
}

// PermissionWindow is approved permission (izin) on a day.
type PermissionWindow struct {
	Type       string
	Start, End time.Time
	Paid       bool
}

// DayInput is everything the evaluation of one attendance day needs.
type DayInput struct {
	Shift          *ShiftWindow
	Off            bool // a day off on the schedule
	Holiday        string
	WorkingWeekday bool // a working day of the work week (used without a schedule)
	Leave          *DayLeave
	Permissions    []PermissionWindow
	Events         []ClockEvent
	Now            time.Time
	Finalize       bool // the day is closed (daily job): missing clock-ins become Absent
	Policy         AttendancePolicy
}

// DayResult is the evaluated attendance day.
type DayResult struct {
	Status                  string
	FirstIn, LastOut        *time.Time
	WorkedMinutes           int
	LateMinutes             int
	EarlyLeaveMinutes       int
	OvertimeMinutes         int
	PermissionMinutes       int
	UnpaidPermissionMinutes int
	LeaveFraction           decimal.Decimal
	Flags                   []string
	Final                   bool
}

func minutesBetween(a, b time.Time) int {
	if !b.After(a) {
		return 0
	}
	return int(b.Sub(a) / time.Minute)
}

func overlapMinutes(a1, a2, b1, b2 time.Time) int {
	s, e := a1, a2
	if b1.After(s) {
		s = b1
	}
	if b2.Before(e) {
		e = b2
	}
	return minutesBetween(s, e)
}

// missingOutGrace is how long after the shift end a clock-out may still
// arrive before the day is flagged Missing Clock-out.
const missingOutGrace = 4 * time.Hour

// breakAfterMinutes: the shift break is deducted from a working span longer
// than 5 hours (a half day keeps its full span).
const breakAfterMinutes = 300

// EvaluateDay matches the clock events of a day to the shift and the
// Attendance Policy: Present / Late / Early Leave / Absent / On Leave / Off
// / Holiday (FR-ATT-03). Late arrival and early leave within the tolerance
// count as on time; approved permission excuses the time it covers.
func EvaluateDay(in DayInput) DayResult {
	out := DayResult{LeaveFraction: decimal.Zero, Flags: []string{}}
	evs := slices.Clone(in.Events)
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].At.Before(evs[j].At) })
	for _, e := range evs {
		if e.Direction == "in" && out.FirstIn == nil {
			t := e.At
			out.FirstIn = &t
		}
	}
	for _, e := range evs {
		if e.Direction == "out" && (out.FirstIn == nil || e.At.After(*out.FirstIn)) {
			t := e.At
			out.LastOut = &t
		}
	}
	if out.FirstIn == nil && out.LastOut != nil {
		out.Flags = append(out.Flags, FlagMissingIn)
	}
	for _, p := range in.Permissions {
		if !p.Paid {
			out.UnpaidPermissionMinutes += minutesBetween(p.Start, p.End)
		}
	}
	span := 0
	if out.FirstIn != nil && out.LastOut != nil {
		span = minutesBetween(*out.FirstIn, *out.LastOut)
	}
	fullLeave := in.Leave != nil && in.Leave.Fraction.GreaterThanOrEqual(decimal.NewFromInt(1))
	if in.Leave != nil {
		out.LeaveFraction = in.Leave.Fraction
	}
	if fullLeave {
		out.Status = DayOnLeave
		out.Final = true
		if out.FirstIn != nil {
			out.Flags = append(out.Flags, FlagWorkedOnLeave)
			out.WorkedMinutes = span
		}
		return out
	}
	if in.Shift == nil {
		switch {
		case out.FirstIn != nil:
			out.Status = DayPresent
			out.WorkedMinutes = span
			out.OvertimeMinutes = span // work on a rest day / without a shift
			out.Flags = append(out.Flags, FlagNoSchedule)
			if out.LastOut == nil && (in.Finalize || in.Now.Sub(*out.FirstIn) > 16*time.Hour) {
				out.Flags = append(out.Flags, FlagMissingOut)
			}
		case in.Holiday != "":
			out.Status = DayHoliday
		default:
			out.Status = DayOff
			if !in.Off && in.WorkingWeekday {
				out.Flags = append(out.Flags, FlagNoSchedule)
			}
		}
		out.Final = true
		return out
	}
	sh := *in.Shift
	start, end := sh.Start, sh.End
	if in.Leave != nil && in.Leave.HalfDay != "" {
		mid := start.Add(end.Sub(start) / 2)
		if in.Leave.HalfDay == "am" {
			start = mid
		} else {
			end = mid
		}
	}
	dayOver := in.Finalize || !in.Now.Before(end.Add(missingOutGrace))
	if out.FirstIn == nil {
		absentAt := start.Add(time.Duration(in.Policy.AbsentAfterMinutes) * time.Minute)
		if in.Finalize || !in.Now.Before(absentAt) || !in.Now.Before(end) {
			out.Status = DayAbsent
			out.Final = dayOver
		} else {
			out.Status = DayScheduled
		}
		return out
	}
	// late arrival (tolerance, excused by permission)
	late := minutesBetween(start, *out.FirstIn)
	for _, p := range in.Permissions {
		covered := overlapMinutes(start, *out.FirstIn, p.Start, p.End)
		late -= covered
		out.PermissionMinutes += covered
	}
	if late <= in.Policy.LateToleranceMinutes {
		late = 0
	}
	out.LateMinutes = max(late, 0)
	if out.LastOut != nil {
		early := minutesBetween(*out.LastOut, end)
		for _, p := range in.Permissions {
			covered := overlapMinutes(*out.LastOut, end, p.Start, p.End)
			early -= covered
			out.PermissionMinutes += covered
		}
		if early <= in.Policy.EarlyLeaveToleranceMinutes {
			early = 0
		}
		out.EarlyLeaveMinutes = max(early, 0)
		worked := span
		if worked > breakAfterMinutes {
			worked -= sh.BreakMinutes
		}
		out.WorkedMinutes = max(worked, 0)
		ot := minutesBetween(sh.End, *out.LastOut) + minutesBetween(*out.FirstIn, sh.Start)
		if in.Holiday != "" {
			ot = out.WorkedMinutes // all work on a public holiday is overtime (PP 35/2021)
		}
		out.OvertimeMinutes = ot
	} else if dayOver {
		out.Flags = append(out.Flags, FlagMissingOut)
	}
	switch {
	case out.LateMinutes > 0:
		out.Status = DayLate
	case out.EarlyLeaveMinutes > 0:
		out.Status = DayEarlyLeave
	default:
		out.Status = DayPresent
	}
	out.Final = dayOver
	return out
}

// DayKindOf returns the overtime day kind of a date (FR-OVT-03): work on a
// public holiday or a rest day uses the rest-day table; with a 6-day week a
// public holiday on the shortest working day (Saturday) uses its own table.
func DayKindOf(holiday, restDay bool, workWeekDays int, weekday time.Weekday) string {
	switch {
	case holiday && workWeekDays == 6 && weekday == time.Saturday:
		return OvertimeShortestDay
	case holiday || restDay:
		return OvertimeRestDay
	default:
		return OvertimeWorkday
	}
}

// IsWorkingWeekday reports whether a weekday is worked in a 5- or 6-day
// week (Monday–Friday, or Monday–Saturday).
func IsWorkingWeekday(weekday time.Weekday, workWeekDays int) bool {
	if weekday == time.Sunday {
		return false
	}
	return weekday != time.Saturday || workWeekDays == 6
}

// Tiers splits overtime hours of one day into multiplier tiers (hour by
// hour, the last chunk may be partial): 3 hours on a workday = 1 h × 1.5 +
// 2 h × 2.
func (p OvertimePolicy) Tiers(hours decimal.Decimal, dayKind string, workWeekDays int) []OvertimeTier {
	steps := p.Steps(dayKind, workWeekDays)
	one := decimal.NewFromInt(1)
	byFactor := map[string]decimal.Decimal{}
	var order []string
	remaining := hours
	for h := 1; remaining.IsPositive(); h++ {
		chunk := decimal.Min(one, remaining)
		factor := "0"
		for _, s := range steps {
			if h >= s.FromHour && (s.ToHour == 0 || h <= s.ToHour) {
				factor = Dec(s.Factor).String()
				break
			}
		}
		if _, ok := byFactor[factor]; !ok {
			order = append(order, factor)
		}
		byFactor[factor] = byFactor[factor].Add(chunk)
		remaining = remaining.Sub(chunk)
	}
	out := make([]OvertimeTier, 0, len(order))
	for _, f := range order {
		out = append(out, OvertimeTier{Factor: f, Hours: byFactor[f]})
	}
	return out
}

// MultipliedOf sums tier hours × factor.
func MultipliedOf(tiers []OvertimeTier) decimal.Decimal {
	total := decimal.Zero
	for _, t := range tiers {
		total = total.Add(t.Hours.Mul(Dec(t.Factor)))
	}
	return total
}

// CountableOvertime rounds actual overtime minutes down to the policy unit
// and drops time below the minimum (FR-OVT-02); the result is in hours.
func (p OvertimePolicy) CountableOvertime(minutes int) decimal.Decimal {
	if minutes < p.MinimumMinutes || minutes <= 0 {
		return decimal.Zero
	}
	if p.RoundingMinutes > 1 {
		minutes = minutes / p.RoundingMinutes * p.RoundingMinutes
	}
	return decimal.NewFromInt(int64(minutes)).Div(decimal.NewFromInt(60)).Round(2)
}

// PayableHours is the approved overtime capped by the countable actual
// overtime of the attendance day and the daily limit (FR-OVT-02).
func (p OvertimePolicy) PayableHours(approved decimal.Decimal, actualMinutes int) decimal.Decimal {
	h := decimal.Min(approved, p.CountableOvertime(actualMinutes))
	if lim := Dec(p.MaxHoursPerDay); lim.IsPositive() && h.GreaterThan(lim) {
		h = lim
	}
	if h.IsNegative() {
		return decimal.Zero
	}
	return h
}

// AddMonths adds months to a date, clamping to the end of the month.
func AddMonths(d time.Time, months int) time.Time {
	y, m, day := d.Date()
	first := time.Date(y, m, 1, 0, 0, 0, 0, d.Location()).AddDate(0, months, 0)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(day, last), 0, 0, 0, 0, d.Location())
}

// AnnualGrant returns when an annual (accrual "annual") leave type grants
// its entitlement in a calendar year: the first time on the day the
// employee completes EligibleAfterMonths of service, then every 1 January
// (§16 #3: 12 days after 12 months). ok is false when the employee is not
// eligible in that year.
func AnnualGrant(lt LeaveType, join time.Time, year int) (grantOn time.Time, ok bool) {
	if lt.Accrual != "annual" || lt.Days <= 0 {
		return time.Time{}, false
	}
	eligible := AddMonths(join, lt.EligibleAfterMonths)
	jan1 := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	if eligible.Year() > year {
		return time.Time{}, false
	}
	if eligible.Before(jan1) || eligible.Equal(jan1) {
		return jan1, true
	}
	return time.Date(eligible.Year(), eligible.Month(), eligible.Day(), 0, 0, 0, 0, time.UTC), true
}

// CarryOverExpiryDate is the date carried-over days lapse in a year (Leave
// Policy carryOverExpiry MM-DD, default 31 March).
func (p LeavePolicy) CarryOverExpiryDate(year int) time.Time {
	m, d := 3, 31
	if parts := strings.Split(p.CarryOverExpiry, "-"); len(parts) == 2 {
		if mm, err := strconv.Atoi(parts[0]); err == nil && mm >= 1 && mm <= 12 {
			m = mm
		}
		if dd, err := strconv.Atoi(parts[1]); err == nil && dd >= 1 && dd <= 31 {
			d = dd
		}
	}
	t := time.Date(year, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
	last := t.AddDate(0, 1, -1).Day()
	return time.Date(year, time.Month(m), min(d, last), 0, 0, 0, 0, time.UTC)
}

// CarryOver is the part of last year's remaining balance carried into the
// new year (at most CarryOverMaxDays).
func (p LeavePolicy) CarryOver(remaining decimal.Decimal) decimal.Decimal {
	if !remaining.IsPositive() {
		return decimal.Zero
	}
	return decimal.Min(remaining, decimal.NewFromInt(int64(max(p.CarryOverMaxDays, 0))))
}

// LeaveType returns a leave type of the policy by code.
func (p LeavePolicy) LeaveType(code string) (LeaveType, bool) {
	for _, t := range p.LeaveTypes {
		if strings.EqualFold(t.Code, code) {
			return t, true
		}
	}
	return LeaveType{}, false
}

// PermissionType returns a permission type of the policy by code.
func (p LeavePolicy) PermissionType(code string) (PermissionType, bool) {
	for _, t := range p.PermissionTypes {
		if strings.EqualFold(t.Code, code) {
			return t, true
		}
	}
	return PermissionType{}, false
}

// HasBalance reports whether a leave type is limited by a yearly balance.
func (lt LeaveType) HasBalance() bool { return lt.Accrual == "annual" }

// DistanceMeters is the great-circle distance between two coordinates.
func DistanceMeters(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLng := (lng2 - lng1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}
