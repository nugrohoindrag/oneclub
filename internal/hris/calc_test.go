package hris

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// EP-08 AC: Rp5,190,000 ÷ 173 = Rp30,000 per hour; 3 hours on a workday =
// 1.5 × 30,000 + 2 × 2 × 30,000 = Rp165,000.
func TestOvertimePayAcceptance(t *testing.T) {
	p := NewOvertimePolicy()
	if got := p.HourlyWage(d("5190000")); !got.Equal(d("30000")) {
		t.Fatalf("hourly wage = %s", got)
	}
	cases := []struct {
		hours, kind string
		week        int
		want        string
	}{
		{"3", OvertimeWorkday, 5, "165000"},
		{"1", OvertimeWorkday, 5, "45000"},
		{"0.5", OvertimeWorkday, 5, "22500"},
		{"1.5", OvertimeWorkday, 5, "75000"},
		// rest day, 5-day week: hours 1–8 2×, 9th 3×, 10th+ 4×
		{"8", OvertimeRestDay, 5, "480000"},
		{"10", OvertimeRestDay, 5, "690000"},
		// rest day, 6-day week: hours 1–7 2×, 8th 3×, 9th+ 4×
		{"9", OvertimeRestDay, 6, "630000"},
		// shortest working day holiday, 6-day week: 1–5 2×, 6th 3×, 7th+ 4×
		{"7", OvertimeShortestDay, 6, "510000"},
		{"0", OvertimeWorkday, 5, "0"},
	}
	for _, c := range cases {
		if got := p.Pay(d("5190000"), d(c.hours), c.kind, c.week); !got.Equal(d(c.want)) {
			t.Errorf("%s h %s (%d-day week) = %s, want %s", c.hours, c.kind, c.week, got, c.want)
		}
	}
}

// EP-09 AC: Rp6,000,000 after 6 months of service = 6/12 × Rp6,000,000.
func TestTHR(t *testing.T) {
	c := NewPayrollConfiguration()
	for _, tc := range []struct {
		months int
		want   string
	}{{6, "3000000"}, {12, "6000000"}, {30, "6000000"}, {1, "500000"}, {0, "0"}} {
		if got := c.THRAmount(d("6000000"), tc.months); !got.Equal(d(tc.want)) {
			t.Errorf("THR after %d months = %s, want %s", tc.months, got, tc.want)
		}
	}
}

func TestServiceMonths(t *testing.T) {
	for _, tc := range []struct {
		join, at string
		want     int
	}{
		{"2026-01-15", "2026-07-15", 6}, {"2026-01-15", "2026-07-14", 5}, {"2025-03-01", "2026-03-01", 12}, {"2026-05-01", "2026-04-01", 0},
		{"2024-01-31", "2024-02-29", 0}, {"2024-01-31", "2024-03-31", 2},
	} {
		if got := ServiceMonths(date(tc.join), date(tc.at)); got != tc.want {
			t.Errorf("ServiceMonths(%s, %s) = %d, want %d", tc.join, tc.at, got, tc.want)
		}
	}
}

// PRD P5 §16 #3: PKWT compensation 1× wage per 12 months, proportional.
func TestPKWTCompensation(t *testing.T) {
	c := NewHRConfiguration()
	if got := c.PKWTCompensation(d("6000000"), 12); !got.Equal(d("6000000")) {
		t.Fatalf("12 months = %s", got)
	}
	if got := c.PKWTCompensation(d("6000000"), 18); !got.Equal(d("9000000")) {
		t.Fatalf("18 months = %s", got)
	}
	if got := c.PKWTCompensation(d("6000000"), 0); !got.IsZero() {
		t.Fatalf("0 months = %s", got)
	}
}

// PP 35/2021: 5 years of service = 6 months severance + 2 months service
// award; resignation earns no severance.
func TestSeverance(t *testing.T) {
	c := NewPayrollConfiguration()
	sev, award := c.SeverancePay(d("10000000"), 5, "terminated")
	if !sev.Equal(d("60000000")) || !award.Equal(d("20000000")) {
		t.Fatalf("5 years terminated = %s + %s", sev, award)
	}
	sev, _ = c.SeverancePay(d("10000000"), 10, "resigned")
	if !sev.IsZero() {
		t.Fatalf("resigned severance = %s", sev)
	}
	sev, award = c.SeverancePay(d("10000000"), 0, "terminated")
	if !sev.Equal(d("10000000")) || !award.IsZero() {
		t.Fatalf("under 1 year = %s + %s", sev, award)
	}
}

// EP-10 AC: Rp6,000,000 → BPJS Kesehatan 240,000 / 60,000, JHT 222,000 /
// 120,000, JP 120,000 / 60,000; the Kesehatan wage cap applies above it.
func TestBPJSContributions(t *testing.T) {
	b := NewPayrollConfiguration().BPJS
	for _, tc := range []struct {
		name   string
		rate   ContributionRate
		wage   string
		er, ee string
	}{
		{"kesehatan", b.Kesehatan, "6000000", "240000", "60000"},
		{"jht", b.JHT, "6000000", "222000", "120000"},
		{"jp", b.JP, "6000000", "120000", "60000"},
		{"kesehatan capped", b.Kesehatan, "20000000", "480000", "120000"},
		{"jp capped", b.JP, "20000000", "210948", "105474"},
	} {
		er, ee := Contribution(tc.rate, d(tc.wage))
		if !er.Equal(d(tc.er)) || !ee.Equal(d(tc.ee)) {
			t.Errorf("%s: %s / %s, want %s / %s", tc.name, er, ee, tc.er, tc.ee)
		}
	}
}

func TestTERAndProgressive(t *testing.T) {
	c := NewPayrollConfiguration()
	for _, tc := range []struct {
		ptkp, gross, want string
	}{
		{"TK/0", "5400000", "0"}, {"TK/0", "5400001", "0.25"}, {"K/0", "10000000", "2"}, {"K/1", "10000000", "1.5"}, {"K/3", "10000000", "1.5"},
		{"TK/0", "2000000000", "34"}, {"UNKNOWN", "6000000", "0.75"},
	} {
		if got := c.TERRate(tc.ptkp, d(tc.gross)); !got.Equal(d(tc.want)) {
			t.Errorf("TER %s %s = %s, want %s", tc.ptkp, tc.gross, got, tc.want)
		}
	}
	// 60,000,000 × 5% + 40,000,000 × 15% = 9,000,000
	if got := ProgressiveTax(c.PPh21.ProgressiveRates, d("100000000")); !got.Equal(d("9000000")) {
		t.Fatalf("Article 17 on 100,000,000 = %s", got)
	}
	if got := ProgressiveTax(c.PPh21.ProgressiveRates, d("0")); !got.IsZero() {
		t.Fatalf("Article 17 on 0 = %s", got)
	}
	// every TER table is ordered and ends without an upper limit
	for cat, table := range c.PPh21.TERRates {
		prev := decimal.Zero
		for i, b := range table {
			if b.UpTo == "" {
				if i != len(table)-1 {
					t.Fatalf("TER %s: open bracket before the end", cat)
				}
				continue
			}
			if !Dec(b.UpTo).GreaterThan(prev) {
				t.Fatalf("TER %s: bracket %d not ascending", cat, i)
			}
			prev = Dec(b.UpTo)
		}
	}
}

// A configured policy keeps the defaults of the fields it leaves out, and
// decoding into a fresh default never changes the package defaults.
func TestPolicyDefaultsAreFresh(t *testing.T) {
	a := NewServiceChargePolicy()
	if err := json.Unmarshal([]byte(`{"departmentShares":{"FNB":"40"},"reservePercent":"10"}`), &a); err != nil {
		t.Fatal(err)
	}
	b := NewServiceChargePolicy()
	if len(b.DepartmentShares) != 0 || b.ReservePercent != "5" || a.DistributedPercent != "95" || a.DepartmentShares["FNB"] != "40" {
		t.Fatalf("defaults shared or lost: %+v / %+v", a, b)
	}
	if NewLeavePolicy().LeaveTypes[0].Days != 12 || NewLeavePolicy().CarryOverMaxDays != 6 || NewLeavePolicy().CarryOverExpiry != "03-31" {
		t.Fatal("annual leave defaults (PRD P5 §16 #3)")
	}
	o := NewOvertimePolicy()
	if o.MaxHoursPerDay != "4" || o.MaxHoursPerWeek != "18" || o.HourlyDivisor != "173" {
		t.Fatal("overtime defaults (PRD P5 §16 #3)")
	}
	if h := NewHRConfiguration(); h.PKWTMaxMonths != 60 || h.ProbationMaxMonths != 3 {
		t.Fatal("PKWT / probation defaults (PRD P5 §16 #3)")
	}
}

func TestESSRegistry(t *testing.T) {
	RegisterESSSection(ESSSection{Key: "zz_test", Label: "Test", Path: "/ops/ess/test", Order: 15})
	RegisterESSSection(ESSSection{Key: "zz_test", Label: "Test 2", Path: "/ops/ess/test", Order: 15})
	var keys []string
	for _, s := range ESSSections() {
		keys = append(keys, s.Key)
		if s.Key == "zz_test" && (s.Label != "Test 2" || s.Permission != PermissionESS) {
			t.Fatalf("replace by key: %+v", s)
		}
	}
	if len(keys) < 5 || keys[0] != "profile" || keys[1] != "zz_test" {
		t.Fatalf("order: %v", keys)
	}
}
