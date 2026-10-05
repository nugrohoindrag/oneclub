package e2e

// PRD P5 EP-22 Advanced Package Management and EP-23 Advanced Tournament
// (with their EP-26/27/28 parts): acceptance of every FR and the mutating
// routes of the area (component rules, capacity & time blocks, inventory
// requirement, profitability, payment templates; team formats and teams,
// registration categories & early-bird, series & Order of Merit, history,
// federation, Member App and website).

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func init() {
	resourceCRUDModules["commercial"] = true
	resourceCRUDModules["golf"] = true
}

// p5Dispatch runs the outbox until cond holds.
func p5Dispatch(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitFor(t, 20*time.Second, what, func() bool {
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		return cond()
	})
}

func p5Local(t *testing.T, ts any) string {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, str(ts))
	if err != nil {
		t.Fatalf("time %v: %v", ts, err)
	}
	return at.In(clubLoc(inst)).Format("15:04")
}

// ── EP-22 Advanced Package Management ─────────────────────────────────────

// PRD P5 EP-22 / §13.1 #8: a multi-business package (golf + spa or tennis
// + dinner) with a choice group, sequence & time gap and service windows
// (FR-PKG-P5-01); package allotment, blackout, component allotment and
// time blocks per date (FR-PKG-P5-02) checked on availability and booking;
// the inventory requirement (BOM) of a large quota, shown and — by the
// Package Policies — enforced (FR-PKG-P5-03); package profitability from
// the allocated revenue, the BOM cost (estimated, then actual), caddy fee,
// commission and other direct costs, equal in the API, the Package
// Profitability Report and the Commercial Performance KPI (FR-PKG-P5-04,
// FR-RPT-P5-03); the payment schedule of the package type template
// (FR-PKG-P5-05).
func TestP5LeisurePackageAdvanced(t *testing.T) {
	sa := superAdmin(t, inst)
	cm := roleUser(t, inst, "club_manager")
	rs := roleUser(t, inst, "reservation_staff")
	fm := roleUser(t, inst, "finance_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	days := pcWeekdays(30, 6)
	d1, d2, d3 := days[0], days[1], days[2]

	// BOM of the dinner (P4 inventory): 2 portions of beef per guest
	uom := idOf(sa.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": "LPU" + sfx, "name": "Portion " + sfx}))
	beef := idOf(sa.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "LPBEEF" + sfx, "name": "Beef " + sfx, "baseUomId": uom,
		"standardCost": "45000"}))
	dinnerProduct := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "LPDN" + sfx, "name": "Dinner " + sfx, "productType": "food",
		"price": "400000"}))
	recipe := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "LPR" + sfx, "name": "Dinner " + sfx, "recipeType": "menu",
		"productId": dinnerProduct, "yieldQuantity": "1", "yieldUomId": uom}))
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": recipe, "itemId": beef, "quantity": "2", "uomId": uom})

	// ── FR-PKG-P5-01 multi-business package with conditional components ──
	pid := idOf(cm.Must(201, "POST", "/api/v1/commercial/packages", map[string]any{"code": "GSD" + sfx, "name": "Golf Spa Day " + sfx, "packageType": "golf_day",
		"pricingMode": "per_pax", "price": "2000000", "minPax": 1, "maxPax": 6, "startTime": "07:00", "allocationMethod": "standalone"}))
	comp := func(body map[string]any) string {
		body["packageId"] = pid
		return idOf(cm.Must(201, "POST", "/api/v1/commercial/package-components", body))
	}
	golf := comp(map[string]any{"seq": 1, "componentType": "service", "name": "Golf 18", "perPax": true, "startTime": "07:00", "durationMinutes": 240,
		"standalonePrice": "1200000", "revenueComponent": "green_fee", "businessLine": "golf"})
	spa := comp(map[string]any{"seq": 2, "componentType": "service", "name": "Spa", "perPax": true, "durationMinutes": 60, "standalonePrice": "500000",
		"revenueComponent": "other", "businessLine": "sportclub"})
	tennis := comp(map[string]any{"seq": 3, "componentType": "service", "name": "Tennis", "perPax": true, "durationMinutes": 60, "standalonePrice": "300000",
		"revenueComponent": "other", "businessLine": "sportclub"})
	dinner := comp(map[string]any{"seq": 4, "componentType": "fnb", "name": "Dinner", "perPax": true, "startTime": "18:30", "durationMinutes": 90,
		"productId": dinnerProduct, "recipeId": recipe, "standalonePrice": "300000", "revenueComponent": "fnb", "businessLine": "pos"})
	// rules: spa or tennis (pick 1), 30–240 minutes after golf, served 10:00–18:00; dinner 18:00–21:00
	cm.Must(422, "POST", "/api/v1/commercial/package-component-rules", map[string]any{"componentId": spa, "afterComponentId": spa})
	cm.Must(422, "POST", "/api/v1/commercial/package-component-rules", map[string]any{"componentId": spa, "minGapMinutes": 60, "maxGapMinutes": 30})
	for _, c := range []string{spa, tennis} {
		cm.Must(201, "POST", "/api/v1/commercial/package-component-rules", map[string]any{"componentId": c, "choiceGroup": "wellness", "choicePick": 1,
			"afterComponentId": golf, "minGapMinutes": 30, "maxGapMinutes": 240, "windowStart": "10:00", "windowEnd": "18:00"})
	}
	dr := idOf(cm.Must(201, "POST", "/api/v1/commercial/package-component-rules", map[string]any{"componentId": dinner, "windowStart": "18:00",
		"windowEnd": "20:00"}))
	cm.Must(200, "PATCH", "/api/v1/commercial/package-component-rules/"+dr, map[string]any{"windowEnd": "21:00", "notes": "Clubhouse kitchen"})
	rules := cm.Must(200, "GET", "/api/v1/commercial/packages/"+pid+"/component-rules", nil).Items()
	if len(rules) != 3 || rules[0]["choiceGroup"] != "WELLNESS" || rules[0]["afterComponent"] != "Golf 18" {
		t.Fatalf("component rules: %v", rules)
	}
	cm.Must(200, "POST", "/api/v1/commercial/packages/"+pid+":publish", nil)

	// ── FR-PKG-P5-02 capacity & time blocks ──────────────────────────────
	capa := func(body map[string]any) string {
		body["packageId"] = pid
		return idOf(cm.Must(201, "POST", "/api/v1/commercial/package-capacities", body))
	}
	cm.Must(422, "POST", "/api/v1/commercial/package-capacities", map[string]any{"packageId": pid, "capacityType": "allotment", "dateFrom": d1})
	cm.Must(422, "POST", "/api/v1/commercial/package-capacities", map[string]any{"packageId": pid, "capacityType": "time_block", "dateFrom": d1, "quota": 2})
	capa(map[string]any{"capacityType": "allotment", "dateFrom": d1, "dateTo": d3, "quota": 4, "basis": "pax", "reason": "Golf Spa Day allotment"})
	capa(map[string]any{"capacityType": "blackout", "dateFrom": d2, "dateTo": d2, "reason": "Club Championship"})
	capa(map[string]any{"componentId": spa, "capacityType": "time_block", "dateFrom": d1, "quota": 2, "basis": "units", "blockMinutes": 60,
		"windowStart": "10:00", "windowEnd": "18:00", "reason": "Two therapists"})
	tennisCap := capa(map[string]any{"componentId": tennis, "capacityType": "allotment", "dateFrom": d3, "dateTo": d3, "quota": 1, "basis": "units"})
	cm.Must(200, "PATCH", "/api/v1/commercial/package-capacities/"+tennisCap, map[string]any{"reason": "One coach"})

	av := rs.Must(200, "GET", "/api/v1/commercial/packages/"+pid+"/availability?date="+d1+"&days=2&pax=2", nil).Items()
	if av[0]["available"] != true || av[1]["available"] != false || !strings.Contains(str(av[1]["reason"]), "Club Championship") {
		t.Fatalf("availability with blackout: %v", av)
	}
	if n := len(asMaps(av[0]["components"])); n != 3 {
		t.Fatalf("availability takes one wellness choice (3 components), got %d", n)
	}

	// FR-PKG-P5-05 payment template of the package type
	tplID := idOf(cm.Must(201, "POST", "/api/v1/commercial/package-payment-templates", map[string]any{"code": "GD" + sfx, "name": "Golf day terms " + sfx,
		"packageType": "golf_day", "minAmount": "1000000", "lines": []map[string]any{{"label": "Deposit", "kind": "down_payment", "percent": "40", "days": 0,
			"from": "booking"}, {"label": "Balance", "kind": "final", "days": 3, "from": "before_start"}}}))
	t.Cleanup(func() { sa.Do("DELETE", "/api/v1/commercial/package-payment-templates/"+tplID, nil) })
	cm.Must(422, "POST", "/api/v1/commercial/package-payment-templates", map[string]any{"code": "GDX" + sfx, "name": "Bad", "packageType": "golf_day",
		"lines": []map[string]any{{"label": "Only", "percent": "40", "days": 0, "from": "tomorrow"}}})

	cust := customer(t, sa, "LPC"+sfx, "Leisure Guest "+sfx, map[string]any{"email": "lp" + sfx + "@leisure.test", "phone": "+62817" + sfx})
	book := func(day string, pax int, addons []string) Resp {
		body := map[string]any{"packageId": pid, "startDate": day, "pax": pax, "customerId": cust}
		if addons != nil {
			body["addons"] = addons
		}
		return rs.Do("POST", "/api/v1/commercial/package-bookings", body, "Idempotency-Key", newKey())
	}
	// both alternatives of a pick-1 group are refused
	if r := book(d1, 1, []string{spa, tennis}); r.Status != 422 || !strings.Contains(string(r.Body), "choice_required") {
		t.Fatalf("choose 1 of WELLNESS: %s", r)
	}
	// booking 1: 2 pax, spa → 11:30 (golf 07:00–11:00 + 30 min gap), block 11:00–12:00 full (2 units)
	r1 := book(d1, 2, []string{spa})
	if r1.Status != 201 {
		t.Fatalf("booking 1: %s", r1)
	}
	b1 := r1.JSON()
	c1 := pkgComponents(b1)
	if len(c1) != 3 || c1["Tennis"] != nil || p5Local(t, c1["Spa"]["scheduledStart"]) != "11:30" || p5Local(t, c1["Dinner"]["scheduledStart"]) != "18:30" {
		t.Fatalf("booking 1 components / schedule: %v", c1)
	}
	// payment template: deposit 40 %, balance 3 days before the start
	sch := b1["schedule"].(map[string]any)
	lines := asMaps(sch["lines"])
	if len(lines) != 2 || !strings.Contains(str(lines[0]["label"]), "GD"+sfx) || !dec(lines[0]["amount"]).Equal(dec(b1["total"]).Mul(decimal.NewFromFloat(0.4)).Round(0)) {
		t.Fatalf("payment template schedule: %v", sch)
	}
	// booking 2: 1 pax, spa moves to the next free time block (12:00)
	r2 := book(d1, 1, nil) // default choice: the first alternative (spa)
	if r2.Status != 201 {
		t.Fatalf("booking 2: %s", r2)
	}
	if c2 := pkgComponents(r2.JSON()); p5Local(t, c2["Spa"]["scheduledStart"]) != "12:00" {
		t.Fatalf("time block: spa moves to 12:00: %v", c2["Spa"])
	}
	// allotment of 4 pax: 3 booked, 2 more are refused
	if r := book(d1, 2, []string{tennis}); r.Status != 409 || !strings.Contains(string(r.Body), "package_allotment_full") {
		t.Fatalf("package allotment: %s", r)
	}
	if r := book(d2, 1, nil); r.Status != 409 || !strings.Contains(string(r.Body), "package_blackout") {
		t.Fatalf("blackout: %s", r)
	}
	// component allotment: one tennis lesson on d3
	if r := book(d3, 1, []string{tennis}); r.Status != 201 {
		t.Fatalf("tennis on d3: %s", r)
	}
	if r := book(d3, 1, []string{tennis}); r.Status != 409 || !strings.Contains(string(r.Body), "component_allotment_full") {
		t.Fatalf("component allotment: %s", r)
	}
	// capacity calendar (GET /commercial/packages/{id}/capacity)
	cal := rs.Must(200, "GET", "/api/v1/commercial/packages/"+pid+"/capacity?date="+d1+"&days=3", nil).JSON()
	cd := asMaps(cal["days"])
	if len(cd) != 3 || cd[0]["pax"].(float64) != 3 || cd[1]["available"] != false {
		t.Fatalf("capacity calendar: %v", cd)
	}
	var allot map[string]any
	for _, c := range asMaps(cd[0]["capacities"]) {
		if c["capacityType"] == "allotment" {
			allot = c
		}
	}
	if allot == nil || allot["used"] != "3" || allot["left"] != "1" {
		t.Fatalf("allotment use: %v", cd[0]["capacities"])
	}
	blocks := 0
	for _, c := range asMaps(cd[0]["components"]) {
		if c["name"] == "Spa" {
			for _, b := range asMaps(c["timeBlocks"]) {
				if b["from"] == "11:00" && b["left"] != "0" || b["from"] == "12:00" && b["used"] != "1" {
					t.Fatalf("spa time blocks: %v", c["timeBlocks"])
				}
				blocks++
			}
		}
	}
	if blocks != 8 {
		t.Fatalf("spa time blocks 10:00–18:00: %d", blocks)
	}

	// ── FR-PKG-P5-03 inventory requirement of the quota ─────────────────
	ir := cm.Must(200, "GET", "/api/v1/commercial/packages/"+pid+"/inventory-requirement?date="+d1, nil).JSON()
	items := asMaps(ir["items"])
	if ir["bookings"].(float64) != 4 || len(items) != 1 || items[0]["required"] != "8" || items[0]["short"] != true || ir["shortages"].(float64) != 1 {
		t.Fatalf("inventory requirement (4 × 2 portions): %v", ir)
	}
	fixPolicy(t, sa, "commercial.package_advanced", func(v map[string]any) { v["inventoryCheck"], v["largeQuotaPax"] = "block", 1 })
	if r := book(d3, 1, nil); r.Status != 409 || !strings.Contains(string(r.Body), "inventory_shortage") {
		t.Fatalf("inventory check (block): %s", r)
	}

	// ── FR-PKG-P5-04 profitability ──────────────────────────────────────
	cost := func(body map[string]any) string {
		body["packageId"] = pid
		return idOf(fm.Must(201, "POST", "/api/v1/commercial/package-cost-rules", body))
	}
	fm.Must(422, "POST", "/api/v1/commercial/package-cost-rules", map[string]any{"packageId": pid, "costType": "commission", "basis": "percent_of_revenue",
		"amount": "120"})
	cost(map[string]any{"componentId": golf, "costType": "caddy_fee", "basis": "per_unit", "amount": "150000"})
	com := cost(map[string]any{"componentId": spa, "costType": "commission", "basis": "percent_of_revenue", "amount": "10"})
	cost(map[string]any{"costType": "other", "basis": "per_booking", "amount": "50000", "description": "Welcome kit"})
	fm.Must(200, "PATCH", "/api/v1/commercial/package-cost-rules/"+com, map[string]any{"description": "Spa therapist commission"})
	// costs were not in force when the bookings were made: recalculate the period
	if r := fm.Must(200, "POST", "/api/v1/commercial/package-profitability:recalculate", map[string]any{"from": d1, "to": d3, "packageId": pid}).JSON(); r["bookings"].(float64) != 3 {
		t.Fatalf("recalculate: %v", r)
	}
	// dinner of booking 1 served: actual BOM cost (2 guests × 2 × 45.000)
	rs.Must(201, "POST", "/api/v1/commercial/package-bookings/"+str(b1["id"])+"/consumption", map[string]any{"bookingComponentId": c1["Dinner"]["id"]},
		"Idempotency-Key", newKey())
	var actual string
	p5Dispatch(t, "actual cost of the dinner", func() bool {
		sysQueryRow(t, inst, `SELECT coalesce((SELECT amount::text FROM commercial.package_cost_lines WHERE booking_component_id = $1 AND basis = 'actual'), '')`,
			[]any{mustUUID(str(c1["Dinner"]["id"]))}, &actual)
		return actual != ""
	})
	if !dec(actual).Equal(decimal.NewFromInt(180000)) {
		t.Fatalf("actual COGS of the dinner: %s", actual)
	}
	prof := fm.Must(200, "GET", "/api/v1/commercial/package-profitability?from="+d1+"&to="+d3+"&packageId="+pid, nil).JSON()
	rows := asMaps(prof["rows"])
	if len(rows) != 1 {
		t.Fatalf("profitability rows: %v", prof)
	}
	pr := rows[0]
	// expected: revenue = allocated net of the 3 bookings; costs:
	// caddy fee 150.000 × 4 golfers, commission 10 % of the spa revenue,
	// welcome kit 50.000 × 3 bookings, dinner COGS 4 × 90.000 (1 actual, 2 estimated bookings)
	var revenue, spaRev string
	sysQueryRow(t, inst, `SELECT coalesce(sum(c.allocated_net), 0)::text, coalesce(sum(round(c.allocated_net * 0.1)) FILTER (WHERE c.name = 'Spa'), 0)::text
		FROM commercial.package_booking_components c JOIN commercial.package_bookings b ON b.id = c.booking_id WHERE b.package_id = $1 AND b.status = 'confirmed'`,
		[]any{mustUUID(pid)}, &revenue, &spaRev)
	wantCost := decimal.NewFromInt(600000).Add(dec(spaRev)).Add(decimal.NewFromInt(150000)).Add(decimal.NewFromInt(360000))
	if pr["bookings"].(float64) != 3 || pr["pax"].(float64) != 4 || !dec(pr["revenue"]).Equal(dec(revenue)) || !dec(pr["totalCost"]).Equal(wantCost) ||
		!dec(pr["cogs"]).Equal(decimal.NewFromInt(360000)) || !dec(pr["caddyFee"]).Equal(decimal.NewFromInt(600000)) ||
		!dec(pr["margin"]).Equal(dec(revenue).Sub(wantCost)) {
		t.Fatalf("profitability %v (revenue %s, cost %s)", pr, revenue, wantCost)
	}
	if byComp := asMaps(fm.Must(200, "GET", "/api/v1/commercial/package-profitability?from="+d1+"&to="+d3+"&packageId="+pid+"&groupBy=component", nil).JSON()["rows"]); len(byComp) != 5 {
		t.Fatalf("profitability per component (golf, spa, tennis, dinner, package): %v", byComp)
	}
	if byMonth := fm.Must(200, "GET", "/api/v1/commercial/package-profitability?from="+d1+"&to="+d3+"&groupBy=month", nil).JSON(); len(asMaps(byMonth["rows"])) == 0 {
		t.Fatalf("profitability per month: %v", byMonth)
	}
	fm.Must(422, "GET", "/api/v1/commercial/package-profitability?groupBy=year", nil)
	// the report equals the API (FR-RPT-P5-03, FR-BI-07)
	rep := fm.Must(200, "GET", "/api/v1/reporting/reports/commercial.package_profitability?params[from]="+d1+"&params[to]="+d3, nil).JSON()
	found := false
	for _, r := range asMaps(rep["rows"]) {
		if r["code"] == "GSD"+sfx {
			found = true
			if !dec(r["margin"]).Equal(dec(pr["margin"])) || !dec(r["revenue"]).Equal(dec(pr["revenue"])) || r["pax"].(float64) != 4 {
				t.Fatalf("report %v vs API %v", r, pr)
			}
		}
	}
	if !found {
		t.Fatalf("Package Profitability Report: %v", rep["rows"])
	}
	dash := sa.Must(200, "GET", "/api/v1/reporting/dashboards/commercial-performance?from="+d1+"&to="+d3, nil).JSON()
	kpi := false
	for _, k := range asMaps(dash["kpis"]) {
		kpi = kpi || k["key"] == "package_margin"
	}
	if !kpi {
		t.Fatalf("Package Margin KPI: %v", dash["kpis"])
	}
	// a cancelled booking keeps no cost (unused components)
	rs.Must(200, "POST", "/api/v1/commercial/package-bookings/"+r2.JSON()["id"].(string)+":cancel", map[string]any{"reason": "guest ill"})
	p5Dispatch(t, "costs of the cancelled booking removed", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM commercial.package_cost_lines WHERE booking_id = $1`, []any{mustUUID(str(r2.JSON()["id"]))}, &n)
		return n == 0
	})
	// roles: profitability for finance, not for reservation staff
	rs.Must(403, "GET", "/api/v1/commercial/package-profitability", nil)
}

// ── EP-23 helpers ─────────────────────────────────────────────────────────

const p5Trn = "/api/v1/golf/tournaments"

// p5Tournament creates and opens a tournament on the course.
func p5Tournament(t *testing.T, c *Client, g golfCourse, name, format string, days []string, extra map[string]any) string {
	t.Helper()
	var rounds []map[string]any
	for _, d := range days {
		rounds = append(rounds, map[string]any{"playDate": d, "startTime": "07:00"})
	}
	body := map[string]any{"name": name, "courseId": g.Course, "playingRouteId": g.RouteAB, "format": format, "scoringBasis": "gross_and_net",
		"fieldSize": 24, "startType": "tee_times", "playersPerFlight": 4, "rounds": rounds}
	for k, v := range extra {
		body[k] = v
	}
	tid := str(c.Must(201, "POST", p5Trn, body).JSON()["id"])
	c.Must(200, "POST", p5Trn+"/"+tid+":open-registration", map[string]any{})
	return tid
}

// p5Guest registers a guest player.
func p5Guest(t *testing.T, c *Client, tid, name, phone, hcp string, consent bool) map[string]any {
	t.Helper()
	body := map[string]any{"guest": map[string]any{"name": name, "phone": phone, "gender": "male"}, "publicConsent": consent}
	if hcp != "" {
		body["handicapIndex"] = hcp
	}
	return c.Must(201, "POST", p5Trn+"/"+tid+"/registrations", body, "Idempotency-Key", newKey()).JSON()
}

// p5StartRound draws, publishes and starts the next round.
func p5StartRound(t *testing.T, c *Client, tid string, body map[string]any) map[string]any {
	t.Helper()
	dr := c.Must(200, "POST", p5Trn+"/"+tid+"/flights:generate", body).JSON()
	c.Must(200, "POST", p5Trn+"/"+tid+":publish-draw", map[string]any{"notify": false})
	c.Must(200, "POST", p5Trn+"/"+tid+":start", nil)
	return dr
}

// p5ValidateRound attests and validates every open card of a round.
func p5ValidateRound(t *testing.T, c *Client, tid string, round int) {
	t.Helper()
	for _, s := range c.Must(200, "GET", fmt.Sprintf("%s/%s/scores?round=%d", p5Trn, tid, round), nil).Items() {
		if s["status"] == "finalized" || s["status"] == "dq" || s["status"] == "wd" || s["status"] == "nr" {
			continue
		}
		c.Must(200, "POST", p5Trn+"/"+tid+"/scores/"+str(s["scoreId"])+":attest", map[string]any{"attestedBy": "Marker"})
		c.Must(200, "POST", p5Trn+"/"+tid+"/scores/"+str(s["scoreId"])+":validate", nil)
	}
}

// p5Card is a full card: strokes = par + over(seq).
func p5Card(pars []int, over func(seq int) int) []map[string]any {
	out := make([]map[string]any, 0, len(pars))
	for i, p := range pars {
		out = append(out, map[string]any{"seq": i + 1, "strokes": p + over(i+1)})
	}
	return out
}

func p5Sum(pars []int, over func(seq int) int) int {
	s := 0
	for i, p := range pars {
		s += p + over(i+1)
	}
	return s
}

// ── EP-23 FR-TRN-P5-01 Team formats ───────────────────────────────────────

// PRD P5 FR-TRN-P5-01 (§16 #11): Scramble and Four-ball from day one,
// Foursomes and Texas Scramble by configuration, with handicap allowances;
// teams formed by hand and automatically (balanced), playing together in
// the draw; the one-ball team card entered once on the Tournament Desk and
// the Four-ball best ball of the members' own cards; team leaderboards
// with the team handicap and the frozen team results.
func TestP5LeisureTeamFormats(t *testing.T) {
	sa := superAdmin(t, inst)
	gm := roleUser(t, inst, "golf_manager")
	ga := roleUser(t, inst, "golf_admin")
	desk := roleUser(t, inst, "starter_marshal")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	g := setupGolfCourse(t, sa, "LT"+sfx[:4])
	pars := trnPars(g.Holes)

	// Team Formats (configuration): WHS allowances by default
	scr := gm.Must(201, "POST", "/api/v1/golf/team-formats", map[string]any{"code": "SCR" + sfx, "name": "Scramble " + sfx, "formatType": "scramble",
		"teamSize": 4}).JSON()
	if fmt.Sprint(scr["allowances"]) != "[25 20 15 10]" {
		t.Fatalf("scramble allowances: %v", scr)
	}
	fb := gm.Must(201, "POST", "/api/v1/golf/team-formats", map[string]any{"code": "FB" + sfx, "name": "Four-ball " + sfx, "formatType": "four_ball",
		"teamSize": 2}).JSON()
	if fmt.Sprint(fb["allowances"]) != "[90]" {
		t.Fatalf("four-ball allowance: %v", fb)
	}
	gm.Must(422, "POST", "/api/v1/golf/team-formats", map[string]any{"code": "FS" + sfx, "name": "Foursomes", "formatType": "foursomes", "teamSize": 4})
	fs := idOf(gm.Must(201, "POST", "/api/v1/golf/team-formats", map[string]any{"code": "FS" + sfx, "name": "Foursomes " + sfx, "formatType": "foursomes",
		"teamSize": 2, "status": "inactive"}))
	tx := gm.Must(201, "POST", "/api/v1/golf/team-formats", map[string]any{"code": "TX" + sfx, "name": "Texas " + sfx, "formatType": "texas_scramble",
		"teamSize": 4, "minDrives": 3}).JSON()
	if fmt.Sprint(tx["allowances"]) != "[10 10 10 10]" {
		t.Fatalf("texas scramble allowances: %v", tx)
	}

	// ── Scramble tournament (one ball) ──────────────────────────────────
	day := clubDay(inst, 40, isSaturday)
	tid := p5Tournament(t, ga, g, "Scramble Cup "+sfx, "stroke_play", []string{day}, nil)
	ga.Must(409, "POST", p5Trn+"/"+tid+"/teams", map[string]any{"registrationIds": []string{}})
	ga.Must(422, "PUT", p5Trn+"/"+tid+"/team-setup", map[string]any{"teamFormatId": fs}) // inactive format
	gm.Must(200, "PATCH", "/api/v1/golf/team-formats/"+fs, map[string]any{"status": "active"})
	ga.Must(200, "PUT", p5Trn+"/"+tid+"/team-setup", map[string]any{"teamFormatId": fs})
	setup := ga.Must(200, "PUT", p5Trn+"/"+tid+"/team-setup", map[string]any{"teamFormatId": scr["id"]}).JSON()
	if setup["format"].(map[string]any)["formatType"] != "scramble" {
		t.Fatalf("team setup: %v", setup)
	}
	var regs []string
	for i := 0; i < 8; i++ {
		r := p5Guest(t, ga, tid, fmt.Sprintf("Scrambler %d %s", i, sfx), fmt.Sprintf("+62878%s%02d", sfx, i), fmt.Sprintf("%d.0", 4+i*4), i%2 == 0)
		regs = append(regs, str(r["id"]))
	}
	t1 := ga.Must(201, "POST", p5Trn+"/"+tid+"/teams", map[string]any{"registrationIds": []string{regs[0], regs[1]}}).JSON()
	ga.Must(200, "PATCH", p5Trn+"/"+tid+"/teams/"+str(t1["id"]), map[string]any{"name": "Eagles " + sfx, "registrationIds": []string{regs[0], regs[3], regs[4],
		regs[7]}, "captainRegistrationId": regs[3]})
	ga.Must(409, "POST", p5Trn+"/"+tid+"/teams", map[string]any{"registrationIds": []string{regs[0], regs[1]}}) // already in a team
	tmp := ga.Must(201, "POST", p5Trn+"/"+tid+"/teams", map[string]any{"registrationIds": []string{regs[1], regs[2]}}).JSON()
	ga.Must(204, "DELETE", p5Trn+"/"+tid+"/teams/"+str(tmp["id"]), nil)
	auto := ga.Must(200, "POST", p5Trn+"/"+tid+"/teams:auto", map[string]any{"method": "balanced"}).JSON()
	teams := asMaps(auto["teams"])
	if len(teams) != 2 || auto["unassigned"].(float64) != 0 || len(asMaps(teams[1]["members"])) != 4 || teams[0]["captainRegistrationId"] != regs[3] {
		t.Fatalf("teams: %v", auto)
	}
	// the draw keeps every team in one flight
	dr := p5StartRound(t, ga, tid, map[string]any{"method": "handicap"})
	teamOf := map[string]string{}
	for _, tm := range teams {
		for _, m := range asMaps(tm["members"]) {
			teamOf[str(m["registrationId"])] = str(tm["id"])
		}
	}
	for _, f := range asMaps(dr["flights"]) {
		seen := map[string]bool{}
		for _, p := range asMaps(f["players"]) {
			seen[teamOf[str(p["registrationId"])]] = true
		}
		if len(seen) != 1 {
			t.Fatalf("a flight mixes teams: %v", f)
		}
	}
	ga.Must(409, "PATCH", p5Trn+"/"+tid+"/teams/"+str(t1["id"]), map[string]any{"name": "Too late"})
	// team cards: entered once on the desk, written on every member's card
	over := map[string]func(int) int{str(teams[0]["id"]): func(s int) int { return s % 3 / 2 }, str(teams[1]["id"]): func(s int) int { return (s + 1) % 2 }}
	for _, tm := range teams {
		res := desk.Must(200, "POST", p5Trn+"/"+tid+"/team-scores", map[string]any{"teamId": tm["id"], "entries": p5Card(pars, over[str(tm["id"])])}).JSON()
		if len(asMaps(res["scorecards"])) != 4 || asMaps(res["scorecards"])[0]["gross"] == nil {
			t.Fatalf("team score: %v", res)
		}
	}
	p5ValidateRound(t, ga, tid, 1)
	lb := ga.Must(200, "GET", p5Trn+"/"+tid+"/team-leaderboard", nil).JSON()
	// expected: team handicap = round(Σ course handicaps (low → high) × 25/20/15/10 %)
	for _, b := range asMaps(lb["boards"]) {
		for _, e := range asMaps(b["entries"]) {
			var chs []int
			for _, tm := range teams {
				if tm["id"] != e["teamId"] {
					continue
				}
				for _, m := range asMaps(tm["members"]) {
					var ch int
					sysQueryRow(t, inst, `SELECT course_handicap FROM golf.tournament_scores WHERE registration_id = $1`, []any{mustUUID(str(m["registrationId"]))}, &ch)
					chs = append(chs, ch)
				}
			}
			for i := range chs { // sort ascending
				for j := i + 1; j < len(chs); j++ {
					if chs[j] < chs[i] {
						chs[i], chs[j] = chs[j], chs[i]
					}
				}
			}
			want := decimal.NewFromInt(int64(chs[0]*25 + chs[1]*20 + chs[2]*15 + chs[3]*10)).Div(decimal.NewFromInt(100)).Round(0).IntPart()
			gross := p5Sum(pars, over[str(e["teamId"])])
			if e["teamHandicap"].(float64) != float64(want) {
				t.Fatalf("team handicap %v, want %d", e, want)
			}
			if b["category"] == "gross" && e["score"].(float64) != float64(gross) || b["category"] == "net" && e["score"].(float64) != float64(gross-int(want)) {
				t.Fatalf("team %s score %v (gross %d, handicap %d)", b["category"], e, gross, want)
			}
		}
	}
	gm.Must(200, "POST", p5Trn+"/"+tid+":finalize", nil)
	p5Dispatch(t, "team results frozen", func() bool {
		return len(ga.Must(200, "GET", p5Trn+"/"+tid+"/team-results", nil).Items()) == 4
	})

	// ── Four-ball tournament (best ball of the members' own cards) ─────
	day2 := clubDay(inst, 47, isSaturday)
	tid2 := p5Tournament(t, ga, g, "Four-ball Cup "+sfx, "stroke_play", []string{day2}, nil)
	ga.Must(200, "PUT", p5Trn+"/"+tid2+"/team-setup", map[string]any{"teamFormatId": fb["id"]})
	var regs2 []string
	for i := 0; i < 4; i++ {
		regs2 = append(regs2, str(p5Guest(t, ga, tid2, fmt.Sprintf("Fourball %d %s", i, sfx), fmt.Sprintf("+62879%s%02d", sfx, i), "", true)["id"]))
	}
	fbTeams := asMaps(ga.Must(200, "POST", p5Trn+"/"+tid2+"/teams:auto", map[string]any{"method": "registration_order"}).JSON()["teams"])
	if len(fbTeams) != 2 {
		t.Fatalf("four-ball teams: %v", fbTeams)
	}
	p5StartRound(t, ga, tid2, map[string]any{})
	ga.Must(409, "POST", p5Trn+"/"+tid2+"/team-scores", map[string]any{"teamId": fbTeams[0]["id"], "entries": p5Card(pars, func(int) int { return 0 })})
	pattern := func(i int) func(int) int { // each player bogeys every other hole
		return func(s int) int { return (s + i) % 2 }
	}
	for i, r := range regs2 {
		ga.Must(200, "POST", p5Trn+"/"+tid2+"/scores", map[string]any{"registrationId": r, "entries": p5Card(pars, pattern(i))})
	}
	p5ValidateRound(t, ga, tid2, 1)
	lb2 := ga.Must(200, "GET", p5Trn+"/"+tid2+"/team-leaderboard", nil).JSON()
	for _, b := range asMaps(lb2["boards"]) {
		if b["category"] != "gross" {
			continue
		}
		for _, e := range asMaps(b["entries"]) {
			// players 0/1 and 2/3 alternate their bogeys: the better ball is par on every hole
			if e["score"].(float64) != float64(p5Sum(pars, func(int) int { return 0 })) || e["positionLabel"] != "T1" {
				t.Fatalf("four-ball best ball: %v", e)
			}
		}
	}
}

// ── EP-23 FR-TRN-P5-02/03/04 Series, Order of Merit, history ─────────────

func p5Standing(t *testing.T, o map[string]any, name string) map[string]any {
	t.Helper()
	for _, e := range asMaps(o["standings"]) {
		if e["playerName"] == name {
			return e
		}
	}
	t.Fatalf("%s not in the Order of Merit: %v", name, o["standings"])
	return nil
}

// PRD P5 §9.6 Tournament Series: a season of three events — two imported
// (history import, FR-TRN-P5-04) and a two-round final with a cut after
// round 1 and re-pairing by position (FR-TRN-P5-03) counting double —
// gives Order of Merit points per position (ties split, missed cut
// participation points), best 2 of 3 events, golf.series_standing_updated
// after every change and a champion in the Hall of Fame when the season is
// completed (FR-TRN-P5-02); the Member App shows the standing and my
// history, the website the leaderboard with names only with consent;
// Tournament History (archive, player statistics, champions), the import of
// past seasons and the Tournament Series Report (FR-RPT-P5-03).
func TestP5LeisureSeriesOrderOfMerit(t *testing.T) {
	sa := superAdmin(t, inst)
	gm := roleUser(t, inst, "golf_manager")
	ga := roleUser(t, inst, "golf_admin")
	pub := anon(t, inst)
	pid := inst.Main.String()
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	g := setupGolfCourse(t, sa, "LS"+sfx[:4])
	pars := trnPars(g.Holes)
	var courseCode string
	sysQueryRow(t, inst, `SELECT code FROM golf.courses WHERE id = $1`, []any{mustUUID(g.Course)}, &courseCode)
	mc, memberCust := trnMemberClient(t, sa)
	var memberCode, memberName string
	sysQueryRow(t, inst, `SELECT coalesce(code, ''), name FROM crm.customers WHERE id = $1`, []any{mustUUID(memberCust)}, &memberCode, &memberName)
	if memberCode == "" {
		memberCode = "LSM" + sfx
		sysExec(t, inst, `UPDATE crm.customers SET code = $2 WHERE id = $1`, mustUUID(memberCust), memberCode)
	}
	andi, rina, joko := "Andi "+sfx, "Rina "+sfx, "Joko "+sfx

	// points table (versioned by the snapshot of the series)
	table := idOf(gm.Must(201, "POST", "/api/v1/golf/series-points-tables", map[string]any{"code": "OM" + sfx, "name": "OoM " + sfx,
		"points": []int{10, 6, 4, 2, 1}, "participationPoints": 1, "tieRule": "split"}))
	// two past events of the season (history import)
	csv := "tournamentRef,tournamentName,startDate,format,courseCode,category,position,positionLabel,playerName,customerCode,score,publicConsent\n" +
		fmt.Sprintf("E1-%[1]s,Series Leg 1 %[1]s,2026-02-07,stroke_play,%[2]s,gross,1,1,%[3]s,%[4]s,74,true\n", sfx, courseCode, memberName, memberCode) +
		fmt.Sprintf("E1-%[1]s,Series Leg 1 %[1]s,2026-02-07,stroke_play,%[2]s,gross,2,2,%[3]s,,76,true\n", sfx, courseCode, andi) +
		fmt.Sprintf("E1-%[1]s,Series Leg 1 %[1]s,2026-02-07,stroke_play,%[2]s,gross,3,3,%[3]s,,78,false\n", sfx, courseCode, rina) +
		fmt.Sprintf("E1-%[1]s,Series Leg 1 %[1]s,2026-02-07,stroke_play,%[2]s,gross,4,4,%[3]s,,80,false\n", sfx, courseCode, joko) +
		fmt.Sprintf("E2-%[1]s,Series Leg 2 %[1]s,2026-04-11,stroke_play,%[2]s,gross,1,1,%[3]s,,73,false\n", sfx, courseCode, rina) +
		fmt.Sprintf("E2-%[1]s,Series Leg 2 %[1]s,2026-04-11,stroke_play,%[2]s,gross,2,T2,%[3]s,%[4]s,75,true\n", sfx, courseCode, memberName, memberCode) +
		fmt.Sprintf("E2-%[1]s,Series Leg 2 %[1]s,2026-04-11,stroke_play,%[2]s,gross,2,T2,%[3]s,,75,true\n", sfx, courseCode, andi) +
		fmt.Sprintf("E2-%[1]s,Series Leg 2 %[1]s,2026-04-11,stroke_play,%[2]s,gross,,MC,%[3]s,,85,false\n", sfx, courseCode, joko)
	if imp := gm.Must(200, "POST", p5Trn+":import", map[string]any{"mode": "commit", "csv": csv}).JSON(); imp["failed"].(float64) != 0 || imp["tournaments"].(float64) != 2 {
		t.Fatalf("history import: %v", imp)
	}
	var e1, e2 string
	sysQueryRow(t, inst, `SELECT id::text FROM golf.tournaments WHERE legacy_ref = $1`, []any{"E1-" + sfx}, &e1)
	sysQueryRow(t, inst, `SELECT id::text FROM golf.tournaments WHERE legacy_ref = $1`, []any{"E2-" + sfx}, &e2)

	// the final: two rounds, cut after round 1 (top 3 and ties), re-pairing by position
	d1 := clubDay(inst, 54, isSaturday)
	d2t, _ := time.Parse("2006-01-02", d1)
	d2 := d2t.AddDate(0, 0, 1).Format("2006-01-02")
	final := p5Tournament(t, ga, g, "Series Final "+sfx, "stroke_play", []string{d1, d2}, map[string]any{"cutAfterRound": 1, "cutTop": 3,
		"playersPerFlight": 2})

	// ── series lifecycle ────────────────────────────────────────────────
	gm.Must(422, "POST", "/api/v1/golf/tournament-series", map[string]any{"name": "No season"})
	s := gm.Must(201, "POST", "/api/v1/golf/tournament-series", map[string]any{"name": "Order of Merit " + sfx, "season": 2026, "pointsTableId": table,
		"bestOf": 2, "minEvents": 1, "public": true}).JSON()
	sid := str(s["id"])
	sb := "/api/v1/golf/tournament-series/" + sid
	gm.Must(200, "PATCH", sb, map[string]any{"description": "Three legs, final counts double"})
	ev1 := gm.Must(201, "POST", sb+"/events", map[string]any{"tournamentId": e1}).JSON()
	gm.Must(201, "POST", sb+"/events", map[string]any{"tournamentId": e2})
	gm.Must(409, "POST", sb+"/events", map[string]any{"tournamentId": e2})
	fin := gm.Must(201, "POST", sb+"/events", map[string]any{"tournamentId": final, "isFinal": true}).JSON()
	if fin["weight"] != "2" {
		t.Fatalf("the final counts double: %v", fin)
	}
	gm.Must(200, "PATCH", sb+"/events/"+str(ev1["id"]), map[string]any{"sequence": 1})
	act := gm.Must(200, "POST", sb+":activate", map[string]any{}).JSON()
	if act["status"] != "active" || act["countedEvents"].(float64) != 2 || act["pointsTable"].(map[string]any)["code"] != "OM"+sfx {
		t.Fatalf("activate: %v", act)
	}
	// points table edits after activation do not change the series (snapshot)
	gm.Must(200, "PATCH", "/api/v1/golf/series-points-tables/"+table, map[string]any{"participationPoints": 3})
	oom := gm.Must(200, "GET", sb+"/order-of-merit", nil).JSON()
	// E1: member 10, Andi 6, Rina 4, Joko 2; E2: Rina 10, member & Andi T2 (6+4)/2 = 5, Joko MC 1
	for name, want := range map[string]string{memberName: "15", rina: "14", andi: "11", joko: "3"} {
		if e := p5Standing(t, oom, name); e["points"] != want {
			t.Fatalf("%s: %v, want %s", name, e, want)
		}
	}
	if p5Standing(t, oom, memberName)["rank"].(float64) != 1 || p5Standing(t, oom, joko)["positionLabel"] != "4" {
		t.Fatalf("ranking: %v", oom["standings"])
	}
	if pcEvents(t, "golf.series_standing_updated", "seriesId", sid) < 1 {
		t.Fatal("golf.series_standing_updated")
	}

	// ── the final (FR-TRN-P5-03): cut and re-pairing by position ────────
	finalRegs := map[string]string{}
	mr := ga.Must(201, "POST", p5Trn+"/"+final+"/registrations", map[string]any{"customerId": memberCust, "handicapIndex": "10.0", "publicConsent": true},
		"Idempotency-Key", newKey()).JSON()
	finalRegs[memberName] = str(mr["id"])
	newcomers := []string{"Tiger " + sfx, "Vijay " + sfx, "Ernie " + sfx, "Retief " + sfx}
	for i, n := range newcomers {
		finalRegs[n] = str(p5Guest(t, ga, final, n, fmt.Sprintf("+62871%s%02d", sfx, i), "12.0", i == 0)["id"])
	}
	over := map[string]int{memberName: 0, newcomers[0]: 1, newcomers[1]: 2, newcomers[2]: 3, newcomers[3]: 4} // strokes over par per 6 holes
	ga.Must(200, "POST", p5Trn+"/"+final+":close-registration", map[string]any{"reason": "Field complete"})
	p5StartRound(t, ga, final, map[string]any{"method": "handicap"})
	for n, rid := range finalRegs {
		k := over[n]
		ga.Must(200, "POST", p5Trn+"/"+final+"/scores", map[string]any{"registrationId": rid, "entries": p5Card(pars, func(s int) int {
			if s%6 < k {
				return 1
			}
			return 0
		})})
	}
	p5ValidateRound(t, ga, final, 1)
	dr2 := p5StartRound(t, ga, final, map[string]any{}) // round 2: standings draw
	if ex, _ := dr2["excluded"].([]any); len(ex) != 2 {
		t.Fatalf("missed the cut: %v", dr2["excluded"])
	}
	fl := asMaps(dr2["flights"])
	last := asMaps(fl[len(fl)-1]["players"])
	if len(fl) != 2 || len(last) != 1 || last[0]["registrationId"] != finalRegs[memberName] {
		t.Fatalf("re-pairing by position (leader out last): %v", fl)
	}
	var made int
	sysQueryRow(t, inst, `SELECT count(*) FROM golf.tournament_registrations WHERE tournament_id = $1 AND made_cut`, []any{mustUUID(final)}, &made)
	if made != 3 {
		t.Fatalf("made the cut: %d", made)
	}
	for _, n := range []string{memberName, newcomers[0], newcomers[1]} {
		ga.Must(200, "POST", p5Trn+"/"+final+"/scores", map[string]any{"registrationId": finalRegs[n], "round": 2, "entries": p5Card(pars, func(s int) int {
			if s%6 < over[n] {
				return 1
			}
			return 0
		})})
	}
	p5ValidateRound(t, ga, final, 2)
	gm.Must(200, "POST", p5Trn+"/"+final+":finalize", nil)
	p5Dispatch(t, "the final counts in the Order of Merit", func() bool {
		return gm.Must(200, "GET", sb, nil).JSON()["countedEvents"].(float64) == 3
	})
	oom = gm.Must(200, "GET", sb+"/order-of-merit", nil).JSON()
	// final ×2: member 20, Tiger 12, Vijay 8, Ernie & Retief MC 2; member best 2 of (10, 5, 20) = 30
	for name, want := range map[string]string{memberName: "30", newcomers[0]: "12", newcomers[1]: "8", newcomers[2]: "2"} {
		if e := p5Standing(t, oom, name); e["points"] != want {
			t.Fatalf("after the final %s: %v, want %s", name, e, want)
		}
	}
	if e := p5Standing(t, oom, memberName); e["events"].(float64) != 3 || len(asMaps(e["eventPoints"])) != 3 {
		t.Fatalf("member events: %v", e)
	}
	if r := gm.Must(200, "POST", sb+":recalculate", map[string]any{}).JSON(); r["points"].(float64) != 13 {
		t.Fatalf("recalculate: %v", r)
	}

	// ── Member App & website ────────────────────────────────────────────
	var mine map[string]any
	for _, x := range mc.Must(200, "GET", "/api/v1/member/golf/tournament-series", nil).Items() {
		if x["id"] == sid {
			mine = x
		}
	}
	if mine == nil || mine["myPosition"] != "1" || mine["myPoints"] != "30" {
		t.Fatalf("member series: %v", mine)
	}
	mo := mc.Must(200, "GET", "/api/v1/member/golf/tournament-series/"+sid+"/order-of-merit", nil).JSON()
	for _, e := range asMaps(mo["standings"]) {
		switch {
		case e["me"] == true:
			if e["playerName"] != memberName {
				t.Fatalf("my line: %v", e)
			}
		case e["customerId"] != nil:
			t.Fatalf("member app exposes another player: %v", e)
		case e["playerName"] == rina || e["playerName"] == joko:
			t.Fatalf("no consent, name shown: %v", e)
		}
	}
	if h := mc.Must(200, "GET", "/api/v1/member/golf/tournament-history", nil).JSON(); len(asMaps(h["results"])) < 3 || len(asMaps(h["series"])) == 0 {
		t.Fatalf("member history: %v", h)
	}
	if ps := pub.Must(200, "GET", "/api/v1/public/tournament-series?propertyId="+pid, nil).Items(); !containsID(ps, sid) {
		t.Fatalf("public series: %v", ps)
	}
	po := pub.Must(200, "GET", "/api/v1/public/tournament-series/"+sid+"/order-of-merit?propertyId="+pid, nil).JSON()
	masked := 0
	for _, e := range asMaps(po["standings"]) {
		if e["customerId"] != nil || e["playerName"] == rina || e["playerName"] == joko {
			t.Fatalf("public leaderboard without consent: %v", e)
		}
		if strings.HasPrefix(str(e["playerName"]), "Player ") {
			masked++
		}
	}
	if masked == 0 {
		t.Fatal("players without consent are masked on the website")
	}

	// ── completion: final standings, champion, Hall of Fame ─────────────
	done := gm.Must(200, "POST", sb+":complete", map[string]any{}).JSON()
	if done["final"] != true || done["champion"] != memberName || len(asMaps(done["standings"])) != 8 {
		t.Fatalf("complete: %v", done)
	}
	var hof int
	sysQueryRow(t, inst, `SELECT count(*) FROM golf.hall_of_fame WHERE title LIKE $1`, []any{"Order of Merit " + sfx + " 2026%"}, &hof)
	if hof != 1 {
		t.Fatalf("Order of Merit champion in the Hall of Fame: %d", hof)
	}
	gm.Must(409, "PATCH", sb, map[string]any{"name": "Changed"})
	p5Dispatch(t, "final standing event", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'golf.series_standing_updated' AND payload->>'seriesId' = $1
			AND (payload->>'final')::boolean`, []any{sid}, &n)
		return n == 1
	})

	// ── Tournament History (FR-TRN-P5-04) ───────────────────────────────
	hist := ga.Must(200, "GET", "/api/v1/golf/tournament-history?q="+sfx, nil).Items()
	if len(hist) != 3 {
		t.Fatalf("history archive: %v", hist)
	}
	for _, h := range hist {
		if len(asMaps(h["champions"])) == 0 || len(h["series"].([]any)) != 1 {
			t.Fatalf("history item: %v", h)
		}
	}
	stats := ga.Must(200, "GET", "/api/v1/golf/tournament-history/players?q="+url.QueryEscape(andi), nil).Items()
	if len(stats) != 1 || stats[0]["events"].(float64) != 2 || stats[0]["top3"].(float64) != 2 {
		t.Fatalf("player statistics: %v", stats)
	}
	ph := ga.Must(200, "GET", "/api/v1/golf/tournament-history/player?customerId="+memberCust, nil).JSON()
	if ph["stats"].(map[string]any)["wins"].(float64) < 2 || len(asMaps(ph["series"])) == 0 {
		t.Fatalf("player history: %v", ph)
	}
	ga.Must(422, "GET", "/api/v1/golf/tournament-history/player", nil)
	champs := ga.Must(200, "GET", "/api/v1/golf/tournament-history/champions?q="+sfx, nil).Items()
	oomChamp := false
	for _, c := range champs {
		oomChamp = oomChamp || (c["kind"] == "order_of_merit" && c["playerName"] == memberName)
	}
	if !oomChamp {
		t.Fatalf("champion history: %v", champs)
	}

	// ── past seasons (import) and the Tournament Series Report ──────────
	past := "seriesCode,seriesName,season,rank,playerName,points,events,wins,publicConsent\n" +
		fmt.Sprintf("OOM24-%[1]s,Order of Merit %[1]s,2024,1,%[2]s,88,6,2,true\nOOM24-%[1]s,Order of Merit %[1]s,2024,2,%[3]s,71,6,1,false\n", sfx, andi, rina)
	if pv := gm.Must(200, "POST", "/api/v1/golf/tournament-series:import", map[string]any{"mode": "preview", "csv": past}).JSON(); pv["series"].(float64) != 1 {
		t.Fatalf("import preview: %v", pv)
	}
	for i := 0; i < 2; i++ {
		res := gm.Must(200, "POST", "/api/v1/golf/tournament-series:import", map[string]any{"mode": "commit", "csv": past}).JSON()
		if res["standings"].(float64) != 2 || res["series"].(float64) != float64(1-i) {
			t.Fatalf("series import %d: %v", i, res)
		}
	}
	old := gm.Must(200, "GET", "/api/v1/golf/tournament-series?season=2024", nil).Items()
	var oldID string
	for _, x := range old {
		if x["code"] == "OOM24-"+sfx {
			oldID = str(x["id"])
			if x["status"] != "completed" || x["source"] != "import" || x["championName"] != andi {
				t.Fatalf("imported season: %v", x)
			}
		}
	}
	if o := gm.Must(200, "GET", "/api/v1/golf/tournament-series/"+oldID+"/order-of-merit", nil).JSON(); len(asMaps(o["standings"])) != 2 {
		t.Fatalf("imported standings: %v", o)
	}
	rep := gm.Must(200, "GET", "/api/v1/reporting/reports/golf.tournament_series?params[from]=2024-01-01&params[to]=2026-12-31&params[series]="+str(s["code"]), nil).JSON()
	if len(asMaps(rep["rows"])) != 8 {
		t.Fatalf("Tournament Series Report: %v", rep["rows"])
	}

	// a draft series: events added and removed, then cancelled
	tmp := gm.Must(201, "POST", "/api/v1/golf/tournament-series", map[string]any{"code": "TMP-" + sfx, "name": "Temporary " + sfx, "season": 2027}).JSON()
	te := gm.Must(201, "POST", "/api/v1/golf/tournament-series/"+str(tmp["id"])+"/events", map[string]any{"tournamentId": e1}).JSON()
	gm.Must(204, "DELETE", "/api/v1/golf/tournament-series/"+str(tmp["id"])+"/events/"+str(te["id"]), nil)
	gm.Must(422, "POST", "/api/v1/golf/tournament-series/"+str(tmp["id"])+":activate", map[string]any{})
	gm.Must(422, "POST", "/api/v1/golf/tournament-series/"+str(tmp["id"])+":cancel", map[string]any{})
	if c := gm.Must(200, "POST", "/api/v1/golf/tournament-series/"+str(tmp["id"])+":cancel", map[string]any{"reason": "Not run"}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancel series: %v", c)
	}
	roleUser(t, inst, "front_desk").Must(403, "GET", "/api/v1/golf/tournament-series", nil)
}

// ── EP-23 FR-TRN-P5-06 advanced registration, FR-TRN-P5-05 federation ────

// p5Members creates customers with an active golf membership (P1 flow).
func p5Members(t *testing.T, sa *Client, sfx string, n int) []string {
	t.Helper()
	program := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "LRP" + sfx, "name": "Golf " + sfx, "programKind": "golf"}))
	typ, pkg := membershipType(t, sa, program, "LRT"+sfx, "Golf Member "+sfx, map[string]any{"golfAccess": true})
	var out []string
	for i := 0; i < n; i++ {
		c := customer(t, sa, fmt.Sprintf("LRM%s%d", sfx, i), fmt.Sprintf("Member %d %s", i, sfx), map[string]any{"phone": fmt.Sprintf("+62876%s%d", sfx, i)})
		activeMembership(t, sa, c, typ, pkg, nil)
		out = append(out, c)
	}
	return out
}

// PRD P5 FR-TRN-P5-06: categories member / guest / sponsor invitation with
// a quota each (a full category waitlists; the waitlist promotes only
// players whose category has room), fees per category and early-bird
// amounts by the registration date. FR-TRN-P5-05 (§16 #11 PGI): official
// handicap indexes entered in bulk are used at registration; the result
// report of a completed tournament is exported and its submission recorded
// (the PGI API is not available).
func TestP5LeisureRegistrationFederation(t *testing.T) {
	sa := superAdmin(t, inst)
	gm := roleUser(t, inst, "golf_manager")
	ga := roleUser(t, inst, "golf_admin")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	g := setupGolfCourse(t, sa, "LR"+sfx[:4])
	day := clubDay(inst, 61, isSaturday)
	tid := p5Tournament(t, ga, g, "Invitational "+sfx, "stableford", []string{day}, nil)
	entry := idOf(ga.Must(201, "POST", p5Trn+"/"+tid+"/fees", map[string]any{"component": "entry_fee", "name": "Tournament Fee", "amount": "500000"}))
	dinner := idOf(ga.Must(201, "POST", p5Trn+"/"+tid+"/fees", map[string]any{"component": "dinner", "name": "Guest dinner", "amount": "150000"}))
	sponsor := idOf(gm.Must(201, "POST", p5Trn+"/"+tid+"/sponsors", map[string]any{"name": "PT Sponsor " + sfx}))
	today := time.Now().In(clubLoc(inst)).Format("2006-01-02")
	yesterday := time.Now().In(clubLoc(inst)).AddDate(0, 0, -1).Format("2006-01-02")
	cats := p5Trn + "/" + tid + "/registration-categories"
	ga.Must(422, "PUT", cats, map[string]any{"quotas": []map[string]any{{"category": "member", "quota": 1}, {"category": "member", "quota": 2}}})
	ga.Must(422, "PUT", cats, map[string]any{"quotas": []map[string]any{}, "fees": []map[string]any{{"feeId": entry, "earlyBirdAmount": "400000"}}})
	ga.Must(200, "PUT", cats, map[string]any{"quotas": []map[string]any{{"category": "member", "quota": 1}, {"category": "guest", "quota": 2},
		{"category": "sponsor_invitation", "quota": 1}}, "fees": []map[string]any{{"feeId": entry, "earlyBirdAmount": "400000", "earlyBirdUntil": today},
		{"feeId": dinner, "category": "guest"}}})
	members := p5Members(t, sa, sfx, 2)
	reg := func(body map[string]any) map[string]any {
		return ga.Must(201, "POST", p5Trn+"/"+tid+"/registrations", body, "Idempotency-Key", newKey()).JSON()
	}
	m1 := reg(map[string]any{"customerId": members[0], "handicapIndex": "8.0"})
	m2 := reg(map[string]any{"customerId": members[1], "handicapIndex": "9.0"})
	if m1["status"] != "registered" || !dec(m1["feeTotal"]).Equal(decimal.NewFromInt(400000)) || m2["status"] != "waitlisted" {
		t.Fatalf("member quota & early-bird: %v / %v", m1, m2)
	}
	g1 := p5Guest(t, ga, tid, "Guest One "+sfx, "+62872"+sfx+"1", "15", false)
	p5Guest(t, ga, tid, "Guest Two "+sfx, "+62872"+sfx+"2", "16", false)
	g3 := p5Guest(t, ga, tid, "Guest Three "+sfx, "+62872"+sfx+"3", "17", false)
	if !dec(g1["feeTotal"]).Equal(decimal.NewFromInt(550000)) || g3["status"] != "waitlisted" {
		t.Fatalf("guests: %v / %v", g1, g3)
	}
	s1 := reg(map[string]any{"guest": map[string]any{"name": "Sponsor Guest " + sfx, "phone": "+62873" + sfx}, "sponsorId": sponsor, "handicapIndex": "20"})
	s2 := reg(map[string]any{"guest": map[string]any{"name": "Sponsor Guest Two " + sfx, "phone": "+62874" + sfx}, "sponsorId": sponsor})
	if s1["status"] != "registered" || !dec(s1["feeTotal"]).Equal(decimal.NewFromInt(400000)) || s2["status"] != "waitlisted" {
		t.Fatalf("sponsor invitation: %v / %v", s1, s2)
	}
	ar := ga.Must(200, "GET", cats, nil).JSON()
	use := map[string]map[string]any{}
	for _, c := range asMaps(ar["categories"]) {
		use[str(c["category"])] = c
	}
	if use["member"]["registered"].(float64) != 1 || use["member"]["waitlisted"].(float64) != 1 || use["guest"]["left"].(float64) != 0 ||
		ar["earlyBirds"].(float64) != 4 {
		t.Fatalf("registration categories: %v", ar)
	}
	// a guest withdraws: the waitlisted member (category full) is skipped, the guest promoted
	wd := ga.Must(200, "POST", p5Trn+"/"+tid+"/registrations/"+str(g1["id"])+":withdraw", map[string]any{"reason": "Business trip"}).JSON()
	if wd["promoted"] != g3["number"] {
		t.Fatalf("promotion by category: %v", wd)
	}
	if r := ga.Must(200, "GET", p5Trn+"/"+tid+"/registrations/"+str(m2["id"]), nil).JSON(); r["status"] != "waitlisted" {
		t.Fatalf("member stays waitlisted: %v", r)
	}
	// early-bird over: guests without quota pay the full fee
	ga.Must(200, "PUT", cats, map[string]any{"quotas": []map[string]any{{"category": "member", "quota": 1}, {"category": "sponsor_invitation", "quota": 1}},
		"fees": []map[string]any{{"feeId": entry, "earlyBirdAmount": "400000", "earlyBirdUntil": yesterday}, {"feeId": dinner, "category": "guest"}}})
	g4 := p5Guest(t, ga, tid, "Guest Four "+sfx, "+62872"+sfx+"4", "18", false)
	if g4["status"] != "registered" || !dec(g4["feeTotal"]).Equal(decimal.NewFromInt(650000)) {
		t.Fatalf("after the early-bird: %v", g4)
	}

	// ── federation: official PGI handicap in bulk, used at registration ─
	fed := customer(t, sa, "LRF"+sfx, "PGI Player "+sfx, map[string]any{"phone": "+62875" + sfx})
	imp := gm.Must(200, "POST", "/api/v1/golf/federation/handicaps:import", map[string]any{"entries": []map[string]any{
		{"customerId": fed, "handicapIndex": "12.3", "federationNumber": "PGI-" + sfx}, {"memberNo": "NOPE-" + sfx, "handicapIndex": "5"},
		{"customerId": fed, "handicapIndex": "99"}}}).JSON()
	if imp["imported"].(float64) != 1 || imp["failed"].(float64) != 2 {
		t.Fatalf("federation handicaps: %v", imp)
	}
	fr := reg(map[string]any{"customerId": fed})
	if fr["handicapIndex"] != "12.3" || fr["handicapSource"] != "federation" {
		t.Fatalf("registration uses the PGI index: %v", fr)
	}
	// result report of a completed tournament (imported history)
	var courseCode string
	sysQueryRow(t, inst, `SELECT code FROM golf.courses WHERE id = $1`, []any{mustUUID(g.Course)}, &courseCode)
	gm.Must(409, "GET", p5Trn+"/"+tid+"/federation-report", nil)
	csv := "tournamentRef,tournamentName,startDate,format,courseCode,category,position,playerName,customerCode,score\n" +
		fmt.Sprintf("PGI-%[1]s,PGI Qualifier %[1]s,2026-06-06,stroke_play,%[2]s,gross,1,PGI Player %[1]s,LRF%[1]s,71\n", sfx, courseCode) +
		fmt.Sprintf("PGI-%[1]s,PGI Qualifier %[1]s,2026-06-06,stroke_play,%[2]s,gross,2,Other %[1]s,,75\n", sfx, courseCode)
	gm.Must(200, "POST", p5Trn+":import", map[string]any{"mode": "commit", "csv": csv})
	var q string
	sysQueryRow(t, inst, `SELECT id::text FROM golf.tournaments WHERE legacy_ref = $1`, []any{"PGI-" + sfx}, &q)
	rep := ga.Must(200, "GET", p5Trn+"/"+q+"/federation-report", nil).JSON()
	if lines := asMaps(rep["lines"]); len(lines) != 2 || lines[0]["position"] != "1" || lines[0]["total"].(float64) != 71 {
		t.Fatalf("federation report: %v", rep)
	}
	if r := gm.Do("POST", p5Trn+"/"+q+"/federation-report:submit", map[string]any{"method": "api"}); r.Status != 409 ||
		!strings.Contains(string(r.Body), "federation_api_unavailable") {
		t.Fatalf("federation API: %s", r)
	}
	sub := gm.Must(200, "POST", p5Trn+"/"+q+"/federation-report:submit", map[string]any{"method": "manual_upload", "reference": "PGI upload " + sfx}).JSON()
	if sub["status"] != "exported" || sub["rows"].(float64) != 2 {
		t.Fatalf("federation submission: %v", sub)
	}
	if rep := ga.Must(200, "GET", p5Trn+"/"+q+"/federation-report", nil).JSON(); len(asMaps(rep["submissions"])) != 1 {
		t.Fatalf("recorded submission: %v", rep["submissions"])
	}
	roleUser(t, inst, "front_desk").Must(403, "POST", "/api/v1/golf/federation/handicaps:import", map[string]any{"entries": []map[string]any{}})
}
