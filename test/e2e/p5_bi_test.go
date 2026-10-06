package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// PRD P5 EP-21 Management Dashboard & BI, EP-27 HR KPI framework and the
// BI part of EP-29: analytics store, Executive Overview = source
// dashboards, targets with approval, drill-down to folio lines, scheduled
// reports per role, report builder, HR Performance and KPI definitions.

func biClient(t *testing.T) *Client {
	t.Helper()
	sa := superAdmin(t, inst)
	sa.Property = inst.MDR
	return sa
}

func biMonth() (string, string, string) {
	now := time.Now().In(clubLoc(inst))
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return first.Format("2006-01-02"), now.Format("2006-01-02"), now.Format("2006-01")
}

func biKPIs(ov map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, d := range ov["domains"].([]any) {
		for _, k := range d.(map[string]any)["kpis"].([]any) {
			km := k.(map[string]any)
			km["domain"] = d.(map[string]any)["code"]
			out[str(km["key"])] = km
		}
	}
	return out
}

func biDec(v any) decimal.Decimal {
	if v == nil {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(str(v))
	if err != nil {
		return decimal.Zero
	}
	return d
}

// biConsistent compares every available executive KPI with its source
// dashboard for the same dates and property (EP-21 AC 1).
func biConsistent(t *testing.T, c *Client, ov map[string]any) (bool, string) {
	t.Helper()
	from, to := str(ov["from"]), str(ov["to"])
	cache := map[string]map[string]any{}
	checked := 0
	for key, k := range biKPIs(ov) {
		if k["status"] != "available" {
			continue
		}
		dash := str(k["dashboard"])
		if _, ok := cache[dash]; !ok {
			path := "/api/v1/reporting/dashboards/" + dash
			if dash == "hr-performance" {
				path = "/api/v1/reporting/hr-performance"
			}
			res := c.Must(200, "GET", path+"?from="+from+"&to="+to, nil).JSON()
			m := map[string]any{}
			for _, x := range res["kpis"].([]any) {
				xm := x.(map[string]any)
				m[str(xm["key"])] = xm["value"]
			}
			cache[dash] = m
		}
		src, ok := cache[dash][str(k["sourceKpi"])]
		if !ok {
			return false, key + ": source KPI missing on " + dash
		}
		if !biDec(src).Equal(biDec(k["value"])) {
			return false, fmt.Sprintf("%s: executive %v ≠ %s %v", key, k["value"], dash, src)
		}
		checked++
	}
	if checked < 30 {
		return false, fmt.Sprintf("only %d KPIs compared", checked)
	}
	return true, ""
}

func TestP5BIExecutiveOverview(t *testing.T) {
	sa := biClient(t)
	from, to, month := biMonth()

	// A golf day at MDR: green fees and a locker on a walk-in folio.
	folio := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "BI Golf Day", "sourceRef": "BI drill-down"}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+folio+"/lines", map[string]any{"chargeType": "other", "description": "Green fee BI", "unitPrice": "1250000",
		"quantity": "2"})
	sa.Must(201, "POST", "/api/v1/billing/folios/"+folio+"/lines", map[string]any{"chargeType": "locker", "description": "Locker BI", "unitPrice": "50000"})
	var line string
	sysQueryRow(t, inst, `SELECT business_line FROM billing.folio_lines WHERE folio_id = $1 LIMIT 1`, []any{mustUUID(folio)}, &line)
	if line != "golf" {
		t.Fatalf("manual charges are golf revenue: %s", line)
	}

	// Before any refresh of the window the analytics store is stale.
	st := sa.Must(200, "GET", "/api/v1/reporting/analytics/status", nil).JSON()
	if st["staleAfterMinutes"].(float64) != 15 {
		t.Fatalf("status: %v", st)
	}
	sa.Must(422, "POST", "/api/v1/reporting/analytics:refresh", map[string]any{"from": "2020-01-01", "to": to})

	// FR-BI-01 / EP-21 AC 1: after the refresh every executive KPI equals
	// its source dashboard for the same dates and property.
	var ov map[string]any
	var why string
	waitFor(t, 60*time.Second, "executive = source dashboards", func() bool {
		// drain the outbox first (best effort: failures of other tests' subscribers are not ours)
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		run := sa.Must(200, "POST", "/api/v1/reporting/analytics:refresh", map[string]any{}).JSON()
		if run["status"] != "completed" || run["error"] != nil || run["kpis"].(float64) < 30 {
			t.Fatalf("refresh: %v", run)
		}
		ov = sa.Must(200, "GET", "/api/v1/reporting/executive", nil).JSON()
		var ok bool
		ok, why = biConsistent(t, sa, ov)
		if !ok {
			t.Log(why)
		}
		return ok
	})
	if ov["month"] != month || ov["from"] != from || ov["to"] != to || ov["stale"] == true || ov["dataAsOf"] == nil {
		t.Fatalf("overview period / freshness: %v %v %v %v %v", ov["month"], ov["from"], ov["to"], ov["stale"], ov["dataAsOf"])
	}
	domains := []string{}
	for _, d := range ov["domains"].([]any) {
		domains = append(domains, str(d.(map[string]any)["code"]))
	}
	if strings.Join(domains, ",") != "golf,sportclub,membership,booking,banquet,commercial,inventory,procurement,finance,crm,hr" {
		t.Fatalf("FR-BI-02 domains: %v", domains)
	}
	kpis := biKPIs(ov)
	golf := kpis["golf_revenue"]
	if biDec(golf["value"]).LessThan(decimal.NewFromInt(2550000)) || golf["indicator"] == nil || golf["previous"] == nil && golf["ytd"] == nil {
		t.Fatalf("golf revenue: %v", golf)
	}
	if bd := golf["breakdown"].([]any); len(bd) < 2 {
		t.Fatalf("golf revenue by component: %v", bd)
	}
	if hc := kpis["headcount"]; hc == nil || hc["status"] != "available" || hc["domain"] != "hr" {
		t.Fatalf("HR headcount: %v", hc)
	}
	if st := sa.Must(200, "GET", "/api/v1/reporting/analytics/status", nil).JSON(); st["stale"] == true || len(st["runs"].([]any)) == 0 {
		t.Fatalf("status after refresh: %v", st)
	}
	// The scheduled refresh job materialises every property.
	if n, err := inst.App.BI.Svc.Refresh(dbtx.System(context.Background()), "incremental"); err != nil || n < 2 {
		t.Fatalf("refresh job: %d %v", n, err)
	}
	// Year to date; per property comparison; trend; KPI definitions.
	yr := sa.Must(200, "GET", "/api/v1/reporting/executive?period=year", nil).JSON()
	if yg := biKPIs(yr)["golf_revenue"]; yr["period"] != "year" || biDec(yg["value"]).LessThan(biDec(golf["value"])) {
		t.Fatalf("year to date: %v", yg)
	}
	sa.Must(422, "GET", "/api/v1/reporting/executive?month=2999-01", nil)
	sa.Must(422, "GET", "/api/v1/reporting/executive?period=week", nil)
	cmp := sa.Must(200, "GET", "/api/v1/reporting/executive/properties?month="+month, nil).JSON()
	if len(cmp["properties"].([]any)) < 2 {
		t.Fatalf("FR-BI-08 per property: %v", cmp["properties"])
	}
	found := false
	for _, k := range cmp["kpis"].([]any) {
		km := k.(map[string]any)
		if km["key"] != "golf_revenue" {
			continue
		}
		for _, v := range km["values"].([]any) {
			vm := v.(map[string]any)
			if vm["propertyId"] == inst.MDR.String() && biDec(vm["value"]).Equal(biDec(golf["value"])) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("per property comparison lacks the MDR golf revenue")
	}
	tr := sa.Must(200, "GET", "/api/v1/reporting/executive/trend?kpi=golf_revenue&months=13", nil).JSON()
	pts := tr["points"].([]any)
	if len(pts) != 13 || !biDec(pts[12].(map[string]any)["value"]).Equal(biDec(golf["value"])) {
		t.Fatalf("trend: %v", tr)
	}
	sa.Must(404, "GET", "/api/v1/reporting/executive/trend?kpi=nope", nil)
	defs := sa.Must(200, "GET", "/api/v1/reporting/kpi-definitions", nil).Items()
	execDefs := 0
	for _, d := range defs {
		if d["executive"] == true {
			execDefs++
		}
		if d["key"] == "golf_revenue" && (d["dashboard"] != "golf-performance" || d["executiveKey"] != "golf_revenue" || d["label"] != "Golf Revenue") {
			t.Fatalf("FR-BI-07 definition: %v", d)
		}
	}
	if execDefs < 35 {
		t.Fatalf("executive KPI definitions: %d", execDefs)
	}

	// EP-21 AC 2: Golf Revenue this month → per day → per component → the
	// folio lines of the source.
	byDay := sa.Must(200, "GET", "/api/v1/reporting/drilldown?kpi=golf_revenue", nil).JSON()
	if !biDec(byDay["total"]).Equal(biDec(golf["value"])) {
		t.Fatalf("drill total %v ≠ KPI %v", byDay["total"], golf["value"])
	}
	sum := decimal.Zero
	for _, r := range byDay["rows"].([]any) {
		sum = sum.Add(biDec(r.(map[string]any)["value"]))
	}
	if !sum.Equal(biDec(golf["value"])) || len(byDay["links"].([]any)) < 2 {
		t.Fatalf("days %s ≠ KPI %v / links %v", sum, golf["value"], byDay["links"])
	}
	// §9.5: by weekday and daypart (weekday PM) the parts add up to the KPI.
	for _, dim := range []string{"weekday", "daypart", "segment", "outlet"} {
		part := sa.Must(200, "GET", "/api/v1/reporting/drilldown?kpi=golf_revenue&by="+dim, nil).JSON()
		psum := decimal.Zero
		for _, r := range part["rows"].([]any) {
			psum = psum.Add(biDec(r.(map[string]any)["value"]))
			if dim == "weekday" && !strings.HasSuffix(str(r.(map[string]any)["label"]), "day") {
				t.Fatalf("weekday label: %v", r)
			}
		}
		if !psum.Equal(biDec(golf["value"])) {
			t.Fatalf("by %s %s ≠ KPI %v", dim, psum, golf["value"])
		}
	}
	sa.Must(422, "GET", "/api/v1/reporting/drilldown?kpi=golf_revenue&filter[weekday]=9", nil)
	isoDow := int(time.Now().In(clubLoc(inst)).Weekday())
	if isoDow == 0 {
		isoDow = 7
	}
	wd := sa.Must(200, "GET", fmt.Sprintf("/api/v1/reporting/drilldown?kpi=golf_revenue&filter[weekday]=%d&by=lines", isoDow), nil).JSON()
	if len(wd["lines"].([]any)) < 2 {
		t.Fatalf("today's weekday lines: %v", wd["lines"])
	}
	byComp := sa.Must(200, "GET", "/api/v1/reporting/drilldown?kpi=golf_revenue&filter[day]="+to+"&by=revenue_component", nil).JSON()
	var locker decimal.Decimal
	for _, r := range byComp["rows"].([]any) {
		if r.(map[string]any)["key"] == "locker" {
			locker = biDec(r.(map[string]any)["value"])
		}
	}
	if locker.LessThan(decimal.NewFromInt(50000)) {
		t.Fatalf("component drill: %v", byComp["rows"])
	}
	lines := sa.Must(200, "GET", "/api/v1/reporting/drilldown?kpi=golf_revenue&filter[day]="+to+"&filter[revenue_component]=locker&by=lines", nil).JSON()
	lsum, mine := decimal.Zero, false
	for _, l := range lines["lines"].([]any) {
		lm := l.(map[string]any)
		lsum = lsum.Add(biDec(lm["total"]))
		if lm["folioId"] == folio && lm["description"] == "Locker BI" {
			mine = true
		}
	}
	if !mine || !lsum.Equal(locker) {
		t.Fatalf("source lines %s ≠ component %s (mine %v)", lsum, locker, mine)
	}
	sa.Must(422, "GET", "/api/v1/reporting/drilldown?kpi=golf_rounds&by=revenue_component", nil)
	if r := sa.Must(200, "GET", "/api/v1/reporting/drilldown?kpi=golf_rounds", nil).JSON(); r["by"] != "day" {
		t.Fatalf("day drill of a stored KPI: %v", r)
	}
	sa.Must(404, "GET", "/api/v1/reporting/drilldown?kpi=unknown", nil)

	// Access: management only.
	roleUser(t, inst, "hr_admin").Must(403, "GET", "/api/v1/reporting/executive", nil)
	roleUser(t, inst, "club_manager").Must(403, "POST", "/api/v1/reporting/analytics:refresh", map[string]any{})
	gm := roleUser(t, inst, "general_manager")
	if g := gm.Must(200, "GET", "/api/v1/reporting/executive", nil).JSON(); len(g["domains"].([]any)) < 10 {
		t.Fatalf("GM overview: %v", g["domains"])
	}
}

// FR-BI-03 / §16 #13: annual budget per month, approval, versions, target
// vs actual with indicator.
func TestP5BITargets(t *testing.T) {
	sa := biClient(t)
	_, _, month := biMonth()
	now := time.Now().In(clubLoc(inst))
	year := now.Year()
	t.Cleanup(func() {
		sysExec(t, inst, `DELETE FROM reporting.kpi_target_plans WHERE property_id = $1 AND year = $2`, inst.MDR, year)
	})
	sa.Must(200, "POST", "/api/v1/reporting/analytics:refresh", map[string]any{})
	ov := sa.Must(200, "GET", "/api/v1/reporting/executive", nil).JSON()
	actual := biDec(biKPIs(ov)["golf_revenue"]["value"])

	sa.Must(422, "POST", "/api/v1/reporting/kpi-targets", map[string]any{"year": year, "targets": []map[string]any{{"kpiKey": "nope", "month": 1, "target": "1"}}})
	sa.Must(422, "POST", "/api/v1/reporting/kpi-targets", map[string]any{"year": year, "targets": []map[string]any{{"kpiKey": "nps", "month": 13, "target": "1"}}})
	// The annual golf revenue budget makes this month's target 8% above the
	// actual so far (pro-rated to the days elapsed).
	days := decimal.NewFromInt(int64(time.Date(year, now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()))
	elapsed := decimal.NewFromInt(int64(now.Day()))
	monthTarget := actual.Mul(decimal.RequireFromString("1.08")).Mul(days).Div(elapsed).Round(0)
	if monthTarget.IsZero() {
		monthTarget = decimal.NewFromInt(1000000)
	}
	plan := sa.Must(201, "POST", "/api/v1/reporting/kpi-targets", map[string]any{"year": year, "title": "BI test budget",
		"annual":  []map[string]any{{"kpiKey": "golf_revenue", "amount": monthTarget.Mul(decimal.NewFromInt(12)).String()}, {"kpiKey": "nps", "amount": "50"}},
		"targets": []map[string]any{{"kpiKey": "cash", "month": int(now.Month()), "target": "1"}}}).JSON()
	pid := str(plan["id"])
	if plan["status"] != "draft" || plan["targetCount"].(float64) != 25 || plan["version"].(float64) != 1 {
		t.Fatalf("plan: %v", plan)
	}
	up := sa.Must(200, "PATCH", "/api/v1/reporting/kpi-targets/"+pid, map[string]any{"title": "Budget MDR", "remove": []string{"cash"},
		"targets": []map[string]any{{"kpiKey": "food_cost_percent", "month": int(now.Month()), "target": "0.3"}}}).JSON()
	if up["title"] != "Budget MDR" || up["targetCount"].(float64) != 25 {
		t.Fatalf("plan update: %v", up)
	}
	// Without a workflow the plan is approved at once.
	ap := sa.Must(200, "POST", "/api/v1/reporting/kpi-targets/"+pid+":submit", nil).JSON()
	if ap["status"] != "approved" || ap["approvalRequestId"] == nil {
		t.Fatalf("submit: %v", ap)
	}
	sa.Must(409, "PATCH", "/api/v1/reporting/kpi-targets/"+pid, map[string]any{"title": "x"})
	sa.Must(409, "DELETE", "/api/v1/reporting/kpi-targets/"+pid, nil)
	ov = sa.Must(200, "GET", "/api/v1/reporting/executive?month="+month, nil).JSON()
	if ov["targetPlan"] == nil || ov["targetPlan"].(map[string]any)["id"] != pid {
		t.Fatalf("plan in force: %v", ov["targetPlan"])
	}
	g := biKPIs(ov)["golf_revenue"]
	if !biDec(g["target"]).Equal(monthTarget) || g["targetToDate"] == nil || g["indicator"] == "no_target" || g["achievement"] == nil ||
		g["ytdTarget"] == nil {
		t.Fatalf("target vs actual: %v", g)
	}
	if !actual.IsZero() && g["indicator"] != "watch" {
		t.Fatalf("8%% below the pro-rated target = watch: %v", g)
	}
	if n := biKPIs(ov)["nps"]; !biDec(n["target"]).Equal(decimal.NewFromInt(50)) {
		t.Fatalf("rate KPI target is the same each month: %v", n)
	}

	// A revision needs the Board: approval workflow on the plans of MDR.
	var approver string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = 'role.platform_admin@matrix.test'`, nil, &approver)
	wf := sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "reporting.kpi_target_plan",
		"name": "Board approval BI " + newKey()[:8], "propertyId": inst.MDR, "steps": []map[string]any{{"stepNo": 1, "name": "Board", "approverType": "user",
			"approverUserId": approver}}}).JSON()
	t.Cleanup(func() {
		superAdmin(t, inst).Do("PATCH", "/api/v1/platform/approval-workflows/"+str(wf["id"]), map[string]any{"status": "inactive"})
	})
	v2 := sa.Must(201, "POST", "/api/v1/reporting/kpi-targets/"+pid+":revise", nil).JSON()
	if v2["version"].(float64) != 2 || v2["status"] != "draft" || v2["targetCount"].(float64) != 25 {
		t.Fatalf("revision: %v", v2)
	}
	sa.Must(200, "PATCH", "/api/v1/reporting/kpi-targets/"+str(v2["id"]), map[string]any{"targets": []map[string]any{{"kpiKey": "golf_revenue",
		"month": int(now.Month()), "target": "1"}}})
	pend := sa.Must(200, "POST", "/api/v1/reporting/kpi-targets/"+str(v2["id"])+":submit", nil).JSON()
	if pend["status"] != "pending_approval" {
		t.Fatalf("pending: %v", pend)
	}
	board := platformAdmin(t, inst)
	board.Property = inst.MDR
	board.Must(200, "POST", "/api/v1/platform/approvals/"+str(pend["approvalRequestId"])+":approve", map[string]any{"reason": "Board OK"})
	if p := sa.Must(200, "GET", "/api/v1/reporting/kpi-targets/"+str(v2["id"]), nil).JSON(); p["status"] != "approved" {
		t.Fatalf("approved v2: %v", p)
	}
	if p := sa.Must(200, "GET", "/api/v1/reporting/kpi-targets/"+pid, nil).JSON(); p["status"] != "superseded" {
		t.Fatalf("v1 superseded: %v", p)
	}
	ov = sa.Must(200, "GET", "/api/v1/reporting/executive", nil).JSON()
	if g := biKPIs(ov)["golf_revenue"]; !biDec(g["target"]).Equal(decimal.NewFromInt(1)) || (!actual.IsZero() && g["indicator"] != "on_track") {
		t.Fatalf("revised target: %v", g)
	}
	// Rejected revision: editable again, then deleted.
	v3 := sa.Must(201, "POST", "/api/v1/reporting/kpi-targets/"+str(v2["id"])+":revise", nil).JSON()
	p3 := sa.Must(200, "POST", "/api/v1/reporting/kpi-targets/"+str(v3["id"])+":submit", nil).JSON()
	board.Must(200, "POST", "/api/v1/platform/approvals/"+str(p3["approvalRequestId"])+":reject", map[string]any{"reason": "Too ambitious"})
	if p := sa.Must(200, "GET", "/api/v1/reporting/kpi-targets/"+str(v3["id"]), nil).JSON(); p["status"] != "rejected" || p["decisionReason"] != "Too ambitious" {
		t.Fatalf("rejected: %v", p)
	}
	sa.Must(204, "DELETE", "/api/v1/reporting/kpi-targets/"+str(v3["id"]), nil)
	list := sa.Must(200, "GET", fmt.Sprintf("/api/v1/reporting/kpi-targets?year=%d", year), nil).Items()
	if len(list) < 2 {
		t.Fatalf("plans: %v", list)
	}
	// The Target vs Actual Report shows the same achievement.
	rep := sa.Must(200, "GET", "/api/v1/reporting/reports/reporting.kpi_target_vs_actual?params[domain]=golf", nil).JSON()
	hit := false
	for _, r := range rep["rows"].([]any) {
		rm := r.(map[string]any)
		if rm["kpi"] == "Golf Revenue" && rm["month"] == month && biDec(rm["target"]).Equal(decimal.NewFromInt(1)) {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("target vs actual report: %v", rep["rows"])
	}
	// Club Manager views targets but cannot set them.
	cm := roleUser(t, inst, "club_manager")
	cm.Must(200, "GET", "/api/v1/reporting/kpi-targets", nil)
	cm.Must(403, "POST", "/api/v1/reporting/kpi-targets", map[string]any{"year": year})
}

// FR-BI-05: scheduled reports by e-mail / in-app per role, under each
// recipient's own report permission (FR-RPT-P5-04).
func TestP5BIScheduledReports(t *testing.T) {
	sa := superAdmin(t, inst) // MAIN
	sa.Must(422, "POST", "/api/v1/reporting/scheduled-reports", map[string]any{"reportCode": "reporting.kpi_target_vs_actual", "format": "pdf",
		"period": "previous_month", "frequency": "monthly", "recipientRoles": []string{"general_manager"}})
	sa.Must(422, "POST", "/api/v1/reporting/scheduled-reports", map[string]any{"reportCode": "reporting.kpi_target_vs_actual", "format": "pdf",
		"period": "previous_month", "frequency": "daily"})
	s := sa.Must(201, "POST", "/api/v1/reporting/scheduled-reports", map[string]any{"name": "BI weekly pack", "reportCode": "reporting.kpi_target_vs_actual",
		"format": "pdf", "period": "month_to_date", "frequency": "weekly", "weekday": 1, "sendTime": "07:00", "channels": []string{"email", "in_app"},
		"recipientRoles": []string{"general_manager", "hr_admin"}}).JSON()
	sid := str(s["id"])
	if s["status"] != "active" || s["nextRunAt"] == nil || s["reportName"] != "KPI Target vs Actual Report" {
		t.Fatalf("schedule: %v", s)
	}
	t.Cleanup(func() { superAdmin(t, inst).Do("DELETE", "/api/v1/reporting/scheduled-reports/"+sid, nil) })
	run := sa.Must(201, "POST", "/api/v1/reporting/scheduled-reports/"+sid+":run", nil).JSON()
	if run["status"] != "partial" || run["delivered"].(float64) < 2 || run["skipped"].(float64) < 1 {
		t.Fatalf("run: %v", run)
	}
	var exports int
	sysQueryRow(t, inst, `SELECT count(*) FROM reporting.exports WHERE schedule_run_id = $1 AND status = 'completed'`, []any{mustUUID(str(run["id"]))}, &exports)
	if exports != int(run["delivered"].(float64)) {
		t.Fatalf("exports %d ≠ delivered %v", exports, run["delivered"])
	}
	gm := roleUser(t, inst, "general_manager")
	var file string
	for _, e := range gm.Must(200, "GET", "/api/v1/reporting/exports", nil).Items() {
		if e["reportCode"] == "reporting.kpi_target_vs_actual" && e["fileUrl"] != nil {
			file = str(e["fileUrl"])
			break
		}
	}
	if file == "" {
		t.Fatal("the GM has no scheduled export")
	}
	if pdf := gm.Must(200, "GET", file, nil); !strings.HasPrefix(string(pdf.Body), "%PDF") {
		t.Fatal("scheduled export is not a PDF")
	}
	var notes int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications WHERE user_id = $1 AND event_code = 'reporting.scheduled_report_ready'`,
		[]any{mustUUID(userID(t, "general_manager"))}, &notes)
	if notes == 0 {
		t.Fatal("no scheduled report notification")
	}
	var hrNotes int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications WHERE user_id = $1 AND event_code = 'reporting.scheduled_report_ready'`,
		[]any{mustUUID(userID(t, "hr_admin"))}, &hrNotes)
	if hrNotes != 0 {
		t.Fatal("a recipient without the report permission received it")
	}
	// Change, pause, resume, and the schedule job delivering a due report.
	up := sa.Must(200, "PATCH", "/api/v1/reporting/scheduled-reports/"+sid, map[string]any{"format": "xlsx", "period": "previous_week",
		"recipientRoles": []string{"general_manager"}}).JSON()
	if up["format"] != "xlsx" || up["period"] != "previous_week" {
		t.Fatalf("update: %v", up)
	}
	if p := sa.Must(200, "POST", "/api/v1/reporting/scheduled-reports/"+sid+":pause", nil).JSON(); p["status"] != "paused" || p["nextRunAt"] != nil {
		t.Fatalf("pause: %v", p)
	}
	sa.Must(409, "POST", "/api/v1/reporting/scheduled-reports/"+sid+":pause", nil)
	if p := sa.Must(200, "POST", "/api/v1/reporting/scheduled-reports/"+sid+":resume", nil).JSON(); p["status"] != "active" || p["nextRunAt"] == nil {
		t.Fatalf("resume: %v", p)
	}
	sysExec(t, inst, `UPDATE reporting.scheduled_reports SET next_run_at = now() - interval '1 minute' WHERE id = $1`, mustUUID(sid))
	// (the scheduled job may deliver it first; either way it runs once)
	if _, err := inst.App.BI.Svc.RunDue(context.Background()); err != nil {
		t.Fatalf("due schedules: %v", err)
	}
	runs := sa.Must(200, "GET", "/api/v1/reporting/scheduled-reports/"+sid+"/runs", nil).Items()
	if len(runs) != 2 || runs[0]["trigger"] != "schedule" || runs[0]["status"] != "completed" {
		t.Fatalf("runs: %v", runs)
	}
	if g := sa.Must(200, "GET", "/api/v1/reporting/scheduled-reports/"+sid, nil).JSON(); g["lastStatus"] != "completed" {
		t.Fatalf("after the job: %v", g)
	} else if next, _ := time.Parse(time.RFC3339, str(g["nextRunAt"])); !next.After(time.Now()) {
		t.Fatalf("next run not advanced: %v", g["nextRunAt"])
	}
	found := false
	for _, x := range sa.Must(200, "GET", "/api/v1/reporting/scheduled-reports", nil).Items() {
		found = found || x["id"] == sid
	}
	if !found {
		t.Fatal("schedule not listed")
	}
	roleUser(t, inst, "hr_admin").Must(403, "GET", "/api/v1/reporting/scheduled-reports", nil)
	sa.Must(204, "DELETE", "/api/v1/reporting/scheduled-reports/"+sid, nil)
	sa.Must(404, "GET", "/api/v1/reporting/scheduled-reports/"+sid, nil)
}

// FR-BI-06: report builder with per-dataset permission; FR-RPT-P5-01 HR
// Performance (registry defaults); FR-BI-02 HR headcount.
func TestP5BIReportBuilderAndHR(t *testing.T) {
	sa := biClient(t)
	sa.Must(200, "POST", "/api/v1/reporting/analytics:refresh", map[string]any{})
	ov := sa.Must(200, "GET", "/api/v1/reporting/executive", nil).JSON()
	golf := biDec(biKPIs(ov)["golf_revenue"]["value"])
	codes := map[string]bool{}
	for _, d := range sa.Must(200, "GET", "/api/v1/reporting/datasets", nil).Items() {
		codes[str(d["code"])] = true
	}
	for _, c := range []string{"revenue", "kpi_daily", "kpi_targets", "golf_rounds", "pos_sales"} {
		if !codes[c] {
			t.Fatalf("dataset %s missing: %v", c, codes)
		}
	}
	res := sa.Must(200, "GET", "/api/v1/reporting/datasets/revenue/query?dimensions=business_line&metrics=amount,lines&period=month_to_date", nil).JSON()
	var g decimal.Decimal
	for _, r := range res["rows"].([]any) {
		if r.(map[string]any)["business_line"] == "golf" {
			g = biDec(r.(map[string]any)["amount"])
		}
	}
	if !g.Equal(golf) {
		t.Fatalf("dataset golf %s ≠ executive %s", g, golf)
	}
	if f := sa.Must(200, "GET", "/api/v1/reporting/datasets/revenue/query?dimensions=revenue_component&metrics=amount&filter[business_line]=golf",
		nil).JSON(); len(f["rows"].([]any)) == 0 || len(f["columns"].([]any)) != 2 {
		t.Fatalf("filtered dataset: %v", f)
	}
	sa.Must(200, "GET", "/api/v1/reporting/datasets/kpi_targets/query?dimensions=kpi&metrics=actual,target,achievement&period=year_to_date", nil)
	sa.Must(200, "GET", "/api/v1/reporting/datasets/golf_rounds/query?dimensions=player_type&metrics=rounds&period=last_30_days", nil)
	sa.Must(200, "GET", "/api/v1/reporting/datasets/pos_sales/query?dimensions=outlet,month&metrics=sales,orders", nil)
	sa.Must(200, "GET", "/api/v1/reporting/datasets/kpi_daily/query?dimensions=kpi&metrics=total,average", nil)
	sa.Must(422, "GET", "/api/v1/reporting/datasets/revenue/query?dimensions=password&metrics=amount", nil)
	sa.Must(422, "GET", "/api/v1/reporting/datasets/revenue/query?dimensions=day", nil)
	sa.Must(404, "GET", "/api/v1/reporting/datasets/nope/query?metrics=amount", nil)
	// per-dataset permission: HR Manager uses the builder but not revenue
	hr := roleUser(t, inst, "hr_manager")
	if r := hr.Do("GET", "/api/v1/reporting/datasets/revenue/query?metrics=amount", nil); r.Status != 403 {
		t.Fatalf("HR Manager on the revenue dataset: %s", r)
	}
	// saved reports
	sv := sa.Must(201, "POST", "/api/v1/reporting/saved-reports", map[string]any{"name": "Golf revenue by component", "dataset": "revenue", "shared": true,
		"definition": map[string]any{"dimensions": []string{"revenue_component"}, "metrics": []string{"amount"}, "filters": map[string]string{"business_line": "golf"},
			"period": "month_to_date"}}).JSON()
	svID := str(sv["id"])
	sa.Must(422, "POST", "/api/v1/reporting/saved-reports", map[string]any{"name": "x", "dataset": "revenue", "definition": map[string]any{"metrics": []string{}}})
	if u := sa.Must(200, "PATCH", "/api/v1/reporting/saved-reports/"+svID, map[string]any{"name": "Golf revenue mix"}).JSON(); u["name"] != "Golf revenue mix" {
		t.Fatalf("saved update: %v", u)
	}
	if r := sa.Must(200, "GET", "/api/v1/reporting/saved-reports/"+svID+"/run", nil).JSON(); len(r["rows"].([]any)) == 0 {
		t.Fatalf("saved run: %v", r)
	}
	listed := false
	for _, x := range sa.Must(200, "GET", "/api/v1/reporting/saved-reports", nil).Items() {
		listed = listed || x["id"] == svID
	}
	if !listed {
		t.Fatal("saved report not listed")
	}
	sa.Must(204, "DELETE", "/api/v1/reporting/saved-reports/"+svID, nil)

	// HR Performance: registry defaults (headcount, caddy attendance and
	// rating live; HRIS KPIs coming soon until registered).
	hp := sa.Must(200, "GET", "/api/v1/reporting/hr-performance", nil).JSON()
	keys := map[string]map[string]any{}
	for _, k := range hp["kpis"].([]any) {
		km := k.(map[string]any)
		keys[str(km["key"])] = km
		if (km["status"] == "available") != (km["value"] != nil) {
			t.Fatalf("HR KPI status / value: %v", km)
		}
	}
	for _, k := range []string{"headcount", "attendance_rate", "overtime_hours", "payroll_cost", "caddy_attendance", "caddy_rating", "turnover",
		"certification_compliance"} {
		if keys[k] == nil {
			t.Fatalf("HR Performance lacks %s", k)
		}
	}
	if !biDec(keys["headcount"]["value"]).Equal(biDec(biKPIs(ov)["headcount"]["value"])) {
		t.Fatalf("headcount %v ≠ executive %v", keys["headcount"]["value"], biKPIs(ov)["headcount"]["value"])
	}
	roleUser(t, inst, "hr_manager").Must(200, "GET", "/api/v1/reporting/hr-performance", nil)
	roleUser(t, inst, "golf_manager").Must(403, "GET", "/api/v1/reporting/hr-performance", nil)
	// The BI reports run and export like every report.
	sa.Must(200, "GET", "/api/v1/reporting/reports/reporting.analytics_refresh", nil)
	sa.Must(202, "POST", "/api/v1/reporting/exports", map[string]any{"reportCode": "reporting.kpi_target_vs_actual", "format": "xlsx"}, "Idempotency-Key", newKey())
}
