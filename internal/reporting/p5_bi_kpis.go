package reporting

// Executive KPI catalogue (FR-BI-02, FR-BI-07): one definition per KPI.
// Every executive KPI points at the KPI of its domain dashboard (P1–P4) and
// runs that very SQL, so the Executive Overview always equals the source
// dashboard and report for the same dates and property; the HR domain is
// filled by the HR KPI registry (EP-27).

import "fmt"

// ExecutiveDomainDef is a domain of the Executive Overview (roadmap §60,
// §66.5).
type ExecutiveDomainDef struct {
	Code, Label, Module, Path string
}

// ExecutiveDomains are the domains of FR-BI-02 in display order.
var ExecutiveDomains = []ExecutiveDomainDef{
	{"golf", "Golf", "golf", "/management/golf"},
	{"sportclub", "Sport Club", "sportclub", "/management/sport-club-performance"},
	{"membership", "Membership", "membership", "/management/membership"},
	{"booking", "Booking", "reservation", "/management/booking"},
	{"banquet", "Banquet & Event", "banquet", "/management/banquet-performance"},
	{"commercial", "Commercial", "commercial", "/management/commercial-performance"},
	{"inventory", "Inventory", "inventory", "/management/inventory-performance"},
	{"procurement", "Procurement", "procurement", "/management/procurement-performance"},
	{"finance", "Finance", "accounting", "/management/financial"},
	{"crm", "CRM", "crm", "/management/crm-performance"},
	{"hr", "HR", "", "/management/hr-performance"},
}

// execKPI is one executive KPI with its resolved definition.
type execKPI struct {
	Key, Label, Domain, Unit, Definition string
	Kind, Direction                      string
	Dashboard, Source, Report            string
	// Revenue is the business line of the revenue drill-down ("*" = all
	// lines); empty = drill by day / property only.
	Revenue    string
	Permission string
	Module     string
	SQL        string
	Breakdown  string
	// Placeholder is a KPI whose definition arrives with a later area.
	Placeholder bool
}

type execSpec struct {
	key, domain, dashboard, source, kind, direction, report, revenue string
}

// execSpecs map the executive KPIs onto the domain dashboards.
var execSpecs = []execSpec{
	{"golf_revenue", "golf", "golf-performance", "golf_revenue", KindFlow, "up", "accounting.revenue_by_business_line", "golf"},
	{"golf_rounds", "golf", "golf-performance", "golf_rounds", KindFlow, "up", "golf.round_history", ""},
	{"tee_time_utilization", "golf", "golf-performance", "tee_time_utilization", KindRate, "up", "", ""},
	{"caddy_utilization", "golf", "golf-performance", "caddy_utilization", KindRate, "up", "golf.caddy_utilization", ""},
	{"sport_revenue", "sportclub", "sport-club-performance", "sport_revenue", KindFlow, "up", "accounting.revenue_by_business_line", "sportclub"},
	{"sport_entries", "sportclub", "sport-club-performance", "entries", KindFlow, "up", "", ""},
	{"court_utilization", "sportclub", "sport-club-performance", "court_utilization", KindRate, "up", "sportclub.court_utilization", ""},
	{"class_attendance", "sportclub", "sport-club-performance", "class_attendance", KindRate, "up", "sportclub.class_attendance", ""},
	{"active_members", "membership", "membership-performance", "active_members", KindCurrent, "up", "membership.lifecycle", ""},
	{"new_members", "membership", "membership-performance", "new_members", KindFlow, "up", "membership.lifecycle", ""},
	{"membership_renewal", "membership", "membership-performance", "renewal", KindFlow, "up", "", ""},
	{"member_revenue", "membership", "membership-performance", "member_revenue", KindFlow, "up", "", ""},
	{"bookings", "booking", "booking-performance", "bookings", KindFlow, "up", "", ""},
	{"bungalow_occupancy", "booking", "booking-performance", "bungalow_occupancy", KindRate, "up", "stay.bungalow_occupancy", ""},
	{"booking_revenue", "booking", "booking-performance", "booking_revenue", KindFlow, "up", "", ""},
	{"booking_cancellation", "booking", "booking-performance", "cancellation", KindFlow, "down", "", ""},
	{"banquet_revenue", "banquet", "banquet-performance", "banquet_revenue", KindFlow, "up", "banquet.revenue", ""},
	{"banquet_events", "banquet", "banquet-performance", "event_count", KindFlow, "up", "banquet.event", ""},
	{"banquet_pax", "banquet", "banquet-performance", "pax", KindFlow, "up", "banquet.event", ""},
	{"total_sales", "commercial", "commercial-performance", "total_sales", KindFlow, "up", "billing.daily_revenue", "*"},
	{"pos_sales", "commercial", "commercial-performance", "pos_sales", KindFlow, "up", "commercial.pos_sales", ""},
	{"average_transaction", "commercial", "commercial-performance", "average_transaction", KindRate, "up", "commercial.pos_sales", ""},
	{"food_cost_percent", "commercial", "commercial-performance", "food_cost_percent", KindRate, "down", "commercial.food_cost", ""},
	{"inventory_value", "inventory", "inventory-performance", "inventory_value", KindStock, "down", "inventory.stock_valuation", ""},
	{"low_stock", "inventory", "inventory-performance", "low_stock", KindCurrent, "down", "inventory.low_stock", ""},
	{"purchase_value", "procurement", "procurement-performance", "purchase_value", KindFlow, "down", "procurement.purchase_order", ""},
	{"outstanding_po", "procurement", "procurement-performance", "outstanding_po", KindStock, "down", "procurement.outstanding_po", ""},
	{"vendor_on_time", "procurement", "procurement-performance", "vendor_on_time", KindRate, "up", "procurement.vendor_performance", ""},
	{"gl_revenue", "finance", "financial-performance", "revenue", KindFlow, "up", "accounting.revenue_by_business_line", ""},
	{"net_income", "finance", "financial-performance", "net_income", KindFlow, "up", "accounting.profit_loss", ""},
	{"accounts_receivable", "finance", "financial-performance", "accounts_receivable", KindStock, "down", "accounting.ar_aging", ""},
	{"accounts_payable", "finance", "financial-performance", "accounts_payable", KindStock, "down", "accounting.ap_aging", ""},
	{"cash", "finance", "financial-performance", "cash", KindStock, "up", "accounting.cash_flow", ""},
	{"nps", "crm", "crm-performance", "nps", KindRate, "up", "crm.nps", ""},
	{"complaint_sla", "crm", "crm-performance", "complaint_sla", KindRate, "up", "crm.complaint", ""},
	{"leads", "crm", "crm-performance", "leads", KindFlow, "up", "crm.lead_source", ""},
	{"deals_won", "crm", "crm-performance", "deals_won", KindFlow, "up", "crm.quotation", ""},
	{"active_customers", "crm", "crm-performance", "active_customers", KindRate, "up", "", ""},
}

// sourceKPI returns the KPI of a domain dashboard.
func sourceKPI(dashboard, key string) (kpiDef, string, bool) {
	d, ok := dashboards[dashboard]
	if !ok {
		return kpiDef{}, "", false
	}
	for _, k := range d.kpis {
		if k.key == key {
			return k, d.name, true
		}
	}
	return kpiDef{}, "", false
}

// executiveKPIs resolves the executive catalogue: the domain KPIs and the
// HR KPIs flagged Executive (placeholders included).
func executiveKPIs() []execKPI {
	out := make([]execKPI, 0, len(execSpecs)+8)
	for _, s := range execSpecs {
		k, _, ok := sourceKPI(s.dashboard, s.source)
		if !ok {
			continue
		}
		out = append(out, execKPI{Key: s.key, Label: k.label, Domain: s.domain, Unit: k.unit, Definition: k.def, Kind: s.kind, Direction: s.direction,
			Dashboard: s.dashboard, Source: s.source, Report: s.report, Revenue: s.revenue, SQL: k.sql, Breakdown: k.breakdown})
	}
	for _, h := range hrKPIs() {
		if !h.Executive {
			continue
		}
		out = append(out, execKPI{Key: h.Key, Label: h.Label, Domain: "hr", Unit: h.Unit, Definition: h.Definition, Kind: h.Kind, Direction: h.Direction,
			Dashboard: "hr-performance", Source: h.Key, Report: h.Report, Permission: h.Permission, Module: h.Module, SQL: h.SQL, Breakdown: h.Breakdown,
			Placeholder: h.SQL == ""})
	}
	return out
}

func executiveKPI(key string) (execKPI, bool) {
	for _, k := range executiveKPIs() {
		if k.Key == key {
			return k, true
		}
	}
	return execKPI{}, false
}

// validateExecutiveCatalogue reports executive KPIs whose source is
// missing (unit test).
func validateExecutiveCatalogue() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range execSpecs {
		if _, _, ok := sourceKPI(s.dashboard, s.source); !ok {
			out = append(out, fmt.Sprintf("%s: %s/%s not found", s.key, s.dashboard, s.source))
		}
		if seen[s.key] {
			out = append(out, s.key+": duplicate key")
		}
		seen[s.key] = true
	}
	return out
}
