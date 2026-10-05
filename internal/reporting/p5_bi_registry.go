package reporting

// EP-27 framework: the P5 areas (HRIS, payroll, advanced CRM, package &
// tournament) register their KPIs, reports and self-service datasets here
// from an init() in their own internal/reporting/p5_<area>.go file:
//
//	func init() {
//		RegisterHRKPI(HRKPI{Key: "headcount", Label: "Headcount", Unit: "count", Kind: KindCurrent, Executive: true,
//			SQL: `SELECT count(*)::text FROM reporting.hr_employees WHERE status = 'active' AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`})
//		RegisterP5Report(sqlReport("hris.headcount", "Headcount Report", "hris", "…", cols(…), nil, 31, `SELECT …`),
//			"property_admin", "general_manager", "hr_manager", "hr_admin")
//		RegisterDataset(Dataset{Code: "hr_attendance", …})
//	}
//
// KPI SQL follows the dashboard convention: one row, one text column; $1 =
// from date, $2 = to date (inclusive), $3 = time zone name; RLS of the
// reporting views narrows to the property. The HR Performance dashboard
// shows the registered KPIs (a registration replaces the default of the
// same key), the Executive Overview shows those flagged Executive and the
// analytics store materialises them like every executive KPI. Reports get
// one permission each, granted to the roles given (salary reports only to
// payroll roles, FR-RPT-P5-04) and exported in CSV / XLSX / PDF.

import (
	"slices"
	"sort"
	"sync"
)

// KPI kinds: how a value behaves over a period.
const (
	KindFlow    = "flow"    // additive over days (revenue, rounds); targets pro-rate within the month
	KindRate    = "rate"    // ratio / average of the period (utilization, NPS)
	KindStock   = "stock"   // balance at the end date (AR, cash, inventory value)
	KindCurrent = "current" // state now, not date dependent (active members, headcount)
)

// HRKPI is a KPI of HR Performance (roadmap §60, FR-RPT-P5-01).
type HRKPI struct {
	Key, Label, Unit, Definition string
	Kind                         string // KindFlow | KindRate | KindStock | KindCurrent
	Direction                    string // "up" (higher is better, default) | "down"
	SQL, Breakdown               string
	// Permission restricts the KPI (e.g. Payroll Cost to payroll roles);
	// ContributePermission adds it to the catalogue when the area does not.
	Permission           string
	ContributePermission bool
	// Module hides the KPI when the module is disabled (e.g. "hris").
	Module string
	// Executive shows the KPI in the HR domain of the Executive Overview
	// (and materialises it in the analytics store).
	Executive bool
	// Report is the source report of the drill-down links.
	Report string
	Order  int
}

// P5 registries (filled from init()).
var (
	regMu         sync.RWMutex
	hrKPIRegistry = map[string]HRKPI{}
	p5ReportList  []*Report
	p5ReportRoles = map[string][]string{}
	datasetList   = map[string]Dataset{}
)

// RegisterHRKPI adds or replaces a KPI of HR Performance.
func RegisterHRKPI(k HRKPI) {
	regMu.Lock()
	defer regMu.Unlock()
	if k.Direction == "" {
		k.Direction = "up"
	}
	if k.Kind == "" {
		k.Kind = KindRate
	}
	hrKPIRegistry[k.Key] = k
}

// RegisterP5Report adds a report of PRD P5 (HR Reports, CRM / package /
// tournament reports, BI reports) with the roles that may run it.
func RegisterP5Report(r *Report, roles ...string) {
	regMu.Lock()
	defer regMu.Unlock()
	for i, x := range p5ReportList {
		if x.Code == r.Code {
			p5ReportList[i] = r
			p5ReportRoles[r.Code] = roles
			return
		}
	}
	p5ReportList = append(p5ReportList, r)
	p5ReportRoles[r.Code] = roles
}

// P5Reports are the reports registered by the P5 areas.
func P5Reports() []*Report {
	regMu.RLock()
	defer regMu.RUnlock()
	return append([]*Report{}, p5ReportList...)
}

// RegisterDataset adds a self-service dataset (FR-BI-06).
func RegisterDataset(d Dataset) {
	regMu.Lock()
	defer regMu.Unlock()
	datasetList[d.Code] = d
}

func datasets() []Dataset {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Dataset, 0, len(datasetList))
	for _, d := range datasetList {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

func datasetByCode(code string) (Dataset, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	d, ok := datasetList[code]
	return d, ok
}

// hrKPIs are the HR Performance KPIs: the defaults of FR-RPT-P5-01 (some
// served from the golf and P0 read models, the rest "coming soon" until an
// HR area registers them) overlaid with the registered ones, in order.
func hrKPIs() []HRKPI {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]HRKPI, 0, len(hrDefaultKPIs)+len(hrKPIRegistry))
	seen := map[string]bool{}
	for _, d := range hrDefaultKPIs {
		if r, ok := hrKPIRegistry[d.Key]; ok {
			if r.Order == 0 {
				r.Order = d.Order
			}
			out = append(out, r)
		} else {
			out = append(out, d)
		}
		seen[d.Key] = true
	}
	for k, r := range hrKPIRegistry {
		if !seen[k] {
			if r.Order == 0 {
				r.Order = 1000
			}
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b HRKPI) int {
		if a.Order != b.Order {
			return a.Order - b.Order
		}
		if a.Key < b.Key {
			return -1
		}
		return 1
	})
	return out
}

// hrDefaultKPIs are the HR Performance KPIs of roadmap §60 and PRD P5
// FR-RPT-P5-01. Headcount reads the P0 employee master until the HRIS
// registers its own; Caddy Attendance and Caddy Rating come from Caddy
// Master (P1–P2); the others arrive with the HR areas (SQL empty = coming
// soon).
var hrDefaultKPIs = []HRKPI{
	{Key: "headcount", Label: "Headcount", Unit: "count", Kind: KindCurrent, Executive: true, Order: 10,
		Definition: "Active employees (employee master)",
		SQL:        `SELECT count(*)::text FROM reporting.bi_employees WHERE status = 'active' AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`,
		Breakdown: `SELECT coalesce(department_name, 'No department') AS label, count(*)::text AS value FROM reporting.bi_employees WHERE status = 'active'
		AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{Key: "attendance_rate", Label: "Attendance", Unit: "ratio", Kind: KindRate, Order: 20, Module: "hris",
		Definition: "Days present ÷ scheduled working days of the period"},
	{Key: "overtime_hours", Label: "Overtime", Unit: "hours", Kind: KindFlow, Direction: "down", Order: 30, Module: "hris",
		Definition: "Approved overtime hours of the period"},
	{Key: "payroll_cost", Label: "Payroll Cost", Unit: "idr", Kind: KindFlow, Direction: "down", Order: 40, Module: "hris",
		Definition: "Gross payroll cost of the payroll runs of the period (employer contributions included)"},
	{Key: "caddy_attendance", Label: "Caddy Attendance", Unit: "ratio", Kind: KindRate, Order: 50,
		Definition: "Caddy attendance records marked present ÷ attendance records of the period",
		SQL: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE status = 'present')::numeric / nullif(count(*), 0), 0), 4))::text
		FROM reporting.golf_caddy_attendance WHERE work_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		Breakdown: `SELECT status AS label, count(*)::text AS value FROM reporting.golf_caddy_attendance WHERE work_date BETWEEN $1::date AND $2::date
		AND $3::text <> '' GROUP BY 1 ORDER BY 1`, Report: "golf.caddy_utilization"},
	{Key: "caddy_rating", Label: "Caddy Rating", Unit: "points", Kind: KindRate, Order: 60,
		Definition: "Average rating (1–5) given to caddies in the period",
		SQL: `SELECT trim_scale(round(coalesce(avg(rating), 0), 2))::text FROM reporting.bi_caddy_ratings
		WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		Breakdown: `SELECT rating::text AS label, count(*)::text AS value FROM reporting.bi_caddy_ratings
		WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY rating ORDER BY rating DESC`},
	{Key: "turnover", Label: "Turnover", Unit: "ratio", Kind: KindRate, Direction: "down", Order: 70, Module: "hris",
		Definition: "Employees who left in the period ÷ average headcount"},
	{Key: "certification_compliance", Label: "Certification Compliance", Unit: "ratio", Kind: KindCurrent, Order: 80, Module: "hris",
		Definition: "Employees in certified positions holding every required valid certification ÷ employees in those positions"},
}
