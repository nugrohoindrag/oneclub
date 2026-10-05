package reporting

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestExecutiveCatalogueResolves(t *testing.T) {
	if bad := validateExecutiveCatalogue(); len(bad) > 0 {
		t.Fatalf("executive KPIs without a source definition: %v", bad)
	}
	seen := map[string]bool{}
	domains := map[string]bool{}
	for _, d := range ExecutiveDomains {
		domains[d.Code] = true
	}
	for _, k := range executiveKPIs() {
		if seen[k.Key] {
			t.Fatalf("duplicate executive KPI %s", k.Key)
		}
		seen[k.Key] = true
		if !domains[k.Domain] {
			t.Fatalf("%s: unknown domain %s", k.Key, k.Domain)
		}
		if !k.Placeholder && k.SQL == "" {
			t.Fatalf("%s: no SQL", k.Key)
		}
		switch k.Kind {
		case KindFlow, KindRate, KindStock, KindCurrent:
		default:
			t.Fatalf("%s: kind %q", k.Key, k.Kind)
		}
	}
	// every domain of FR-BI-02 has at least one KPI
	for _, d := range ExecutiveDomains {
		n := 0
		for _, k := range executiveKPIs() {
			if k.Domain == d.Code {
				n++
			}
		}
		if n == 0 {
			t.Fatalf("domain %s has no KPI", d.Code)
		}
	}
}

func TestSchedulePeriod(t *testing.T) {
	wed := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) // Wednesday
	cases := []struct {
		period   string
		from, to string
	}{
		{"previous_day", "2026-10-06", "2026-10-06"},
		{"previous_week", "2026-09-28", "2026-10-04"},
		{"previous_month", "2026-09-01", "2026-09-30"},
		{"month_to_date", "2026-10-01", "2026-10-07"},
		{"year_to_date", "2026-01-01", "2026-10-07"},
	}
	for _, c := range cases {
		f, to := SchedulePeriod(c.period, wed)
		if f.Format(dateFmt) != c.from || to.Format(dateFmt) != c.to {
			t.Fatalf("%s: %s – %s, want %s – %s", c.period, f.Format(dateFmt), to.Format(dateFmt), c.from, c.to)
		}
	}
	// a Monday's previous week ends yesterday
	mon := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if f, to := SchedulePeriod("previous_week", mon); f.Format(dateFmt) != "2026-09-28" || to.Format(dateFmt) != "2026-10-04" {
		t.Fatalf("monday previous week: %s – %s", f, to)
	}
	// January's previous month is December of the year before
	jan := time.Date(2027, 1, 3, 0, 0, 0, 0, time.UTC)
	if f, to := SchedulePeriod("previous_month", jan); f.Format(dateFmt) != "2026-12-01" || to.Format(dateFmt) != "2026-12-31" {
		t.Fatalf("january previous month: %s – %s", f, to)
	}
}

func TestNextScheduledRun(t *testing.T) {
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	at := time.Date(2026, 10, 7, 8, 0, 0, 0, jkt) // Wednesday 08:00 WIB
	one, three := 1, 3
	cases := []struct {
		freq     string
		weekday  *int
		monthDay *int
		send     string
		want     time.Time
	}{
		{"daily", nil, nil, "07:00", time.Date(2026, 10, 8, 7, 0, 0, 0, jkt)},
		{"daily", nil, nil, "09:30", time.Date(2026, 10, 7, 9, 30, 0, 0, jkt)},
		{"weekly", &one, nil, "07:00", time.Date(2026, 10, 12, 7, 0, 0, 0, jkt)},
		{"monthly", nil, &three, "08:00", time.Date(2026, 11, 3, 8, 0, 0, 0, jkt)},
	}
	for _, c := range cases {
		got := NextScheduledRun(c.freq, c.weekday, c.monthDay, c.send, at, jkt)
		if !got.Equal(c.want) {
			t.Fatalf("%s %s: %s, want %s", c.freq, c.send, got.In(jkt), c.want)
		}
	}
	// December monthly rolls into January
	dec := time.Date(2026, 12, 20, 0, 0, 0, 0, jkt)
	if got := NextScheduledRun("monthly", nil, &three, "08:00", dec, jkt); !got.Equal(time.Date(2027, 1, 3, 8, 0, 0, 0, jkt)) {
		t.Fatalf("december monthly: %s", got.In(jkt))
	}
}

func TestExpandTargets(t *testing.T) {
	lines, err := expandTargets([]KPITargetLine{{KPIKey: "nps", Month: 1, Target: "45"}},
		[]KPITargetAnnual{{KPIKey: "golf_revenue", Amount: "1000000000.10"}, {KPIKey: "tee_time_utilization", Amount: "0.65"}})
	if err != nil {
		t.Fatal(err)
	}
	sum := decimal.Zero
	n := 0
	for _, l := range lines {
		switch l.KPIKey {
		case "golf_revenue":
			v, _ := decimal.NewFromString(l.Target)
			sum = sum.Add(v)
			n++
		case "tee_time_utilization":
			if l.Target != "0.65" {
				t.Fatalf("rate KPI is not split: %v", l)
			}
		}
	}
	if n != 12 || sum.String() != "1000000000.1" {
		t.Fatalf("even split of a flow KPI: %d months, sum %s", n, sum)
	}
	if _, err := expandTargets([]KPITargetLine{{KPIKey: "unknown", Month: 1, Target: "1"}}, nil); err == nil {
		t.Fatal("unknown KPI accepted")
	}
	if _, err := expandTargets([]KPITargetLine{{KPIKey: "nps", Month: 13, Target: "1"}}, nil); err == nil {
		t.Fatal("month 13 accepted")
	}
	if _, err := expandTargets([]KPITargetLine{{KPIKey: "nps", Month: 2, Target: "x"}}, nil); err == nil {
		t.Fatal("non-decimal target accepted")
	}
	if _, err := expandTargets([]KPITargetLine{{KPIKey: "golf_revenue", Month: 1, Target: "1"}},
		[]KPITargetAnnual{{KPIKey: "golf_revenue", Amount: "12"}}); err == nil {
		t.Fatal("duplicate month accepted")
	}
	if _, err := expandTargets(nil, []KPITargetAnnual{{KPIKey: "turnover", Amount: "1"}}); err == nil {
		t.Fatal("placeholder KPI accepted")
	}
}

func TestIndicator(t *testing.T) {
	pol := DefaultBIPolicy()
	d := decimal.RequireFromString
	cases := []struct {
		value, target, dir, want, ach string
	}{
		{"920", "1000", "up", "watch", "0.92"},
		{"1000", "1000", "up", "on_track", "1"},
		{"850", "1000", "up", "off_track", "0.85"},
		{"0.30", "0.32", "down", "on_track", "1.0667"},
		{"0.35", "0.32", "down", "watch", "0.9143"},
		{"0", "0.32", "down", "on_track", "1"},
		{"-50", "-100", "up", "on_track", "1.5"},
	}
	for _, c := range cases {
		ind, ach := indicatorFor(d(c.value), d(c.target), c.dir, pol)
		if ind != c.want || ach == nil || *ach != c.ach {
			t.Fatalf("%s vs %s (%s): %s %v, want %s %s", c.value, c.target, c.dir, ind, *ach, c.want, c.ach)
		}
	}
}

func TestHRKPIRegistry(t *testing.T) {
	before := hrKPIs()
	var head HRKPI
	for _, k := range before {
		if k.Key == "headcount" {
			head = k
		}
	}
	if head.SQL == "" || !head.Executive {
		t.Fatalf("headcount default: %+v", head)
	}
	for _, k := range before {
		if k.Key == "turnover" && k.SQL != "" {
			t.Fatal("turnover must be coming soon until an HR area registers it")
		}
	}
	RegisterHRKPI(HRKPI{Key: "turnover", Label: "Turnover", Unit: "ratio", Kind: KindRate, SQL: "SELECT '1'", Executive: true})
	RegisterHRKPI(HRKPI{Key: "zz_test_kpi", Label: "Test", Unit: "count", SQL: "SELECT '2'"})
	t.Cleanup(func() {
		regMu.Lock()
		delete(hrKPIRegistry, "turnover")
		delete(hrKPIRegistry, "zz_test_kpi")
		regMu.Unlock()
	})
	after := hrKPIs()
	if len(after) != len(before)+1 {
		t.Fatalf("registry: %d → %d", len(before), len(after))
	}
	var pc HRKPI
	for i, k := range after {
		if k.Key == "turnover" {
			pc = k
			if after[i+1].Key != "certification_compliance" {
				t.Fatalf("a replaced KPI keeps its position: %v", after[i+1].Key)
			}
		}
	}
	if pc.SQL != "SELECT '1'" || pc.Direction != "up" {
		t.Fatalf("registered KPI: %+v", pc)
	}
	if after[len(after)-1].Key != "zz_test_kpi" {
		t.Fatalf("new KPIs come last: %s", after[len(after)-1].Key)
	}
	if _, ok := executiveKPI("turnover"); !ok {
		t.Fatal("executive HR KPI not in the executive catalogue")
	}
}

func TestDatasetDefinitions(t *testing.T) {
	d, ok := datasetByCode("revenue")
	if !ok {
		t.Fatal("revenue dataset")
	}
	if err := validateDefinition(d, DatasetDefinition{Dimensions: []string{"business_line"}, Metrics: []string{"amount"}}); err != nil {
		t.Fatal(err)
	}
	if err := validateDefinition(d, DatasetDefinition{Dimensions: []string{"password"}, Metrics: []string{"amount"}}); err == nil {
		t.Fatal("unknown dimension accepted")
	}
	if err := validateDefinition(d, DatasetDefinition{Metrics: []string{"amount; DROP TABLE x"}}); err == nil {
		t.Fatal("unknown metric accepted")
	}
	if err := validateDefinition(d, DatasetDefinition{Metrics: []string{"amount"}, Filters: map[string]string{"x": "1"}}); err == nil {
		t.Fatal("unknown filter accepted")
	}
	today := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	f, to, err := resolveDefinition(DatasetDefinition{Period: "previous_month"}, today)
	if err != nil || f.Format(dateFmt) != "2026-09-01" || to.Format(dateFmt) != "2026-09-30" {
		t.Fatalf("previous month: %s %s %v", f, to, err)
	}
	if _, _, err := resolveDefinition(DatasetDefinition{From: "2026-10-05", To: "2026-10-01"}, today); err == nil {
		t.Fatal("inverted range accepted")
	}
	for _, ds := range datasets() {
		if ds.Permission == "" || len(ds.Metrics) == 0 || ds.Source == "" || ds.DateExpr == "" {
			t.Fatalf("dataset %s incomplete", ds.Code)
		}
	}
}

func TestP5ContributionGrantsKnownPermissions(t *testing.T) {
	c := P5Contribution()
	perms := map[string]bool{}
	for _, p := range c.Permissions {
		if perms[p.Code] {
			t.Fatalf("duplicate permission %s", p.Code)
		}
		perms[p.Code] = true
	}
	for _, r := range P5Reports() {
		if !perms[r.Permission] {
			t.Fatalf("report %s permission %s not contributed", r.Code, r.Permission)
		}
	}
	if !perms[KPITargetReport.Permission] || len(c.RolePermissions["general_manager"]) == 0 {
		t.Fatal("BI report permission or GM grants missing")
	}
}
