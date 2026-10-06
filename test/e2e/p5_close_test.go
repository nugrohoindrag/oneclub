package e2e

// PRD P5 close-out follow-ups: payroll totals in the HR migration
// reconciliation (FR-MIG-P5-05/06), the asset custodian on the offboarding
// checklist (FR-HR-06) and BPJS differences of retro corrections (FR-PAY
// retro, FR-TAX-HR-03). Each test works on a property of its own, so its
// payroll periods have no runs of other tests.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// closeProperty creates a property and a department with a position on it.
func closeProperty(t *testing.T, prefix string) prTeam {
	t.Helper()
	base := superAdmin(t, inst)
	sfx := hrSuffix()
	prop := base.Must(201, "POST", "/api/v1/platform/properties", map[string]any{"code": prefix + sfx, "name": "Close-out " + prefix + " " + sfx,
		"timezone": "Asia/Jakarta"}).JSON()
	c := *base
	c.Property = mustUUID(str(prop["id"]))
	// the instance-wide HR KPIs of other tests (headcount of the HRIS master) do not count these employees
	t.Cleanup(func() {
		sysExec(t, inst, `UPDATE hris.employees SET archived_at = now() WHERE property_id = $1`, c.Property)
	})
	tm := prTeam{sfx: sfx, sa: &c, unitCode: "CL" + sfx}
	tm.unitID = idOf(c.Must(201, "POST", hrBase+"/org-units", map[string]any{"code": tm.unitCode, "name": "Close-out " + sfx, "unitType": "department",
		"costCenter": "CC-" + sfx}, "Idempotency-Key", newKey()))
	tm.posID = idOf(c.Must(201, "POST", hrBase+"/positions", map[string]any{"code": "CLP" + sfx, "name": "Close-out Officer", "orgUnitId": tm.unitID},
		"Idempotency-Key", newKey()))
	return tm
}

// closeRole logs a matrix user in with a role at the property of tm.
func closeRole(t *testing.T, tm prTeam, role string) *Client {
	t.Helper()
	sysExec(t, inst, `INSERT INTO platform.role_assignments (id, user_id, role_id, property_id)
		SELECT gen_random_uuid(), u.id, r.id, $1 FROM platform.users u, platform.roles r WHERE u.email = $2 AND r.code = $3 AND r.is_template
		ON CONFLICT DO NOTHING`, tm.sa.Property, "role."+role+"@matrix.test", role)
	t.Cleanup(func() {
		sysExec(t, inst, `DELETE FROM platform.role_assignments WHERE property_id = $1 AND user_id = (SELECT id FROM platform.users WHERE email = $2)`,
			tm.sa.Property, "role."+role+"@matrix.test")
	})
	c := roleUser(t, inst, role)
	c.Property = tm.sa.Property
	return c
}

// closeRun creates, calculates and approves a regular run of a month (no
// approval workflow at the property: approved at once).
func closeRun(t *testing.T, tm prTeam, body map[string]any) map[string]any {
	t.Helper()
	run := tm.sa.Must(201, "POST", hrBase+"/payroll-runs", body, "Idempotency-Key", newKey()).JSON()
	tm.sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(run["id"])+":calculate", map[string]any{})
	run = tm.sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(run["id"])+":approve", map[string]any{}).JSON()
	if run["status"] != "approved" {
		t.Fatalf("run %v", run)
	}
	return run
}

// FR-PAY retro: a raise approved later with effect from an approved period
// is paid by the adjustment run of the next period with the differences of
// the BPJS contributions (caps of the corrected period). Worked example of
// internal/hris TestPayrollRetroBPJSDifferences: 11,000,000 → 13,000,000.
func TestP5CloseRetroBPJS(t *testing.T) {
	tm := closeProperty(t, "RB")
	sa := tm.sa
	sa.Must(200, "POST", hrBase+"/pay-components:load-defaults", map[string]any{})
	fin := closeRole(t, tm, "finance_manager")
	q := prMonth(3, nil)
	next := q.AddDate(0, 1, 0)
	e := prEmployee(t, tm, "Rina", "TK/0", q.AddDate(-2, 0, 0), "11000000", nil, "BCA", true, nil)
	run := closeRun(t, tm, map[string]any{"runType": "regular", "periodCode": q.Format("2006-01"), "orgUnitId": tm.unitID})
	s := prSlip(t, sa, str(run["id"]), e)
	prEq(t, "paid JHT employee", prLine(s, "BPJS_JHT_EE"), di(220_000))
	prEq(t, "paid Kesehatan employer", prLine(s, "BPJS_KESEHATAN_ER"), di(440_000))

	// the raise: a position allowance of 2,000,000 from the first day of the paid period
	st := sa.Must(201, "POST", hrBase+"/salary-structures", map[string]any{"code": "RAISE-" + tm.sfx, "name": "Raise Rina", "scope": "employee",
		"employeeId": e, "effectiveFrom": ymdOf(q), "lines": []map[string]any{{"componentCode": "POSITION", "amount": "2000000"}}},
		"Idempotency-Key", newKey()).JSON()
	fin.Must(200, "POST", hrBase+"/salary-structures/"+str(st["id"])+":activate", map[string]any{"note": "retro raise"})

	adj := sa.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "adjustment", "periodCode": next.Format("2006-01"),
		"correctsPeriod": q.Format("2006-01"), "orgUnitId": tm.unitID}, "Idempotency-Key", newKey()).JSON()
	adj = sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(adj["id"])+":calculate", map[string]any{}).JSON()
	a := prSlip(t, sa, str(adj["id"]), e)
	prEq(t, "retro position allowance", prLine(a, "POSITION"), di(2_000_000))
	want := map[string]int64{"BPJS_KESEHATAN_EE": 10_000, "BPJS_KESEHATAN_ER": 40_000, "BPJS_JHT_EE": 40_000, "BPJS_JHT_ER": 74_000, "BPJS_JKK_ER": 4_800,
		"BPJS_JKM_ER": 6_000}
	seen := map[string]bool{}
	for _, l := range a["lines"].([]any) {
		m := l.(map[string]any)
		code := str(m["code"])
		if !strings.HasPrefix(code, "BPJS_") {
			continue
		}
		seen[code] = true
		w, ok := want[code]
		if !ok || !dec(m["amount"]).Equal(di(w)) || m["source"] != "retro" || !strings.Contains(str(m["description"]), q.Format("2006-01")) {
			t.Fatalf("BPJS difference %s: %v (want %d)", code, m, w)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("BPJS differences %v, want %v (JP is capped both times)", seen, want)
	}
	prEq(t, "BPJS employee of the correction", a["bpjsEmployee"], di(50_000))
	prEq(t, "BPJS employer of the correction", a["bpjsEmployer"], di(124_800))
	var deductible string
	sysQueryRow(t, inst, `SELECT deductible::text FROM hris.payroll_slips WHERE id = $1`, []any{mustUUID(str(a["id"]))}, &deductible)
	prEq(t, "deductible JHT (annual PPh 21)", deductible, di(40_000))
	adj = sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(adj["id"])+":approve", map[string]any{}).JSON()
	if adj["status"] != "approved" {
		t.Fatalf("adjustment: %v", adj)
	}
	// corrected once: a second correction of the period finds no difference
	again := sa.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "adjustment", "periodCode": next.Format("2006-01"),
		"correctsPeriod": q.Format("2006-01"), "orgUnitId": tm.unitID}, "Idempotency-Key", newKey()).JSON()
	again = sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(again["id"])+":calculate", map[string]any{}).JSON()
	if s2 := prSlip(t, sa, str(again["id"]), e); !dec(s2["gross"]).IsZero() || !dec(s2["bpjsEmployee"]).IsZero() || !dec(s2["bpjsEmployer"]).IsZero() {
		t.Fatalf("second correction: %v", s2)
	}
	sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(again["id"])+":cancel", map[string]any{"note": "nothing to correct"})
}

// FR-MIG-P5-05/06: the gross and net pay of the last legacy period against
// the OneClub parallel run are control totals of the HR migration
// reconciliation, next to headcount and leave balances.
func TestP5ClosePayrollReconciliation(t *testing.T) {
	tm := closeProperty(t, "RP")
	sa := tm.sa
	metrics := map[string]bool{}
	for _, m := range sa.Must(200, "GET", hrBase+"/migration-reconciliation-metrics", nil).Items() {
		metrics[str(m["code"])] = true
	}
	if !metrics["payroll_gross"] || !metrics["payroll_net"] || !metrics["headcount"] {
		t.Fatalf("metrics: %v", metrics)
	}
	p := prMonth(2, nil)
	period := p.Format("2006-01")
	a := prEmployee(t, tm, "Agus", "TK/0", p.AddDate(-1, 0, 0), "8000000", nil, "BCA", true, nil)
	prEmployee(t, tm, "Budi", "K/1", p.AddDate(-1, 0, 0), "6000000", nil, "BCA", true, nil)
	run := sa.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "regular", "periodCode": period}, "Idempotency-Key", newKey()).JSON()
	run = sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(run["id"])+":calculate", map[string]any{}).JSON() // a parallel run stays calculated
	gross, net := dec(run["gross"]), dec(run["net"])
	if !gross.IsPositive() || !net.IsPositive() {
		t.Fatalf("run: %v", run)
	}
	// the legacy payroll of the period (parallel run)
	var aNo string
	sysQueryRow(t, inst, `SELECT employee_no FROM hris.employees WHERE id = $1`, []any{mustUUID(a)}, &aNo)
	sa.Must(200, "POST", hrBase+"/payroll-imports", map[string]any{"kind": "legacy", "periodCode": period,
		"csv": fmt.Sprintf("employeeNo,componentCode,amount\n%s,GROSS,8000000\n%s,NET,7500000\n", aNo, aNo)})

	cutover := hrDay(0)
	rec := sa.Must(201, "POST", hrBase+"/migration-reconciliations", map[string]any{"cutoverDate": cutover, "legacySystem": "Legacy payroll " + tm.sfx,
		"lines": []map[string]any{
			{"metric": "payroll_gross", "key": "TOTAL", "legacy": gross.String()},
			{"metric": "payroll_net", "key": "TOTAL", "legacy": net.Sub(di(1000)).String()},
			{"metric": "payroll_gross", "key": period, "legacy": gross.String()},
			{"metric": "payroll_net", "key": time.Now().AddDate(0, 6, 0).Format("2006-01"), "legacy": "0"},
		}}, "Idempotency-Key", newKey()).JSON()
	if rec["checks"] != float64(4) || rec["mismatches"] != float64(1) {
		t.Fatalf("reconciliation: %v", rec)
	}
	for _, l := range rec["lines"].([]any) {
		m := l.(map[string]any)
		switch str(m["metric"]) + "|" + str(m["key"]) {
		case "payroll_gross|TOTAL", "payroll_gross|" + period:
			if !dec(m["oneclub"]).Equal(gross) || m["match"] != true {
				t.Fatalf("gross line %v (run gross %s)", m, gross)
			}
		case "payroll_net|TOTAL":
			if !dec(m["oneclub"]).Equal(net) || !dec(m["difference"]).Equal(di(1000)) || m["match"] != false {
				t.Fatalf("net line %v (run net %s)", m, net)
			}
		default:
			if !dec(m["oneclub"]).IsZero() || m["match"] != true {
				t.Fatalf("period without a run: %v", m)
			}
		}
	}
	// the CSV of the sign-off lists the payroll totals
	if r := sa.Do("GET", hrBase+"/migration-reconciliations/"+str(rec["id"])+"/csv", nil); r.Status != 200 ||
		!strings.Contains(string(r.Body), "payroll_net,TOTAL,") {
		t.Fatalf("csv %d %s", r.Status, r.Body)
	}
	// a cancelled run no longer counts
	sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(run["id"])+":cancel", map[string]any{"note": "parallel run repeated"})
	rec = sa.Must(200, "POST", hrBase+"/migration-reconciliations/"+str(rec["id"])+":recalculate", map[string]any{}).JSON()
	for _, l := range rec["lines"].([]any) {
		if m := l.(map[string]any); strings.HasPrefix(str(m["metric"]), "payroll_") && !dec(m["oneclub"]).IsZero() {
			t.Fatalf("cancelled run counted: %v", m)
		}
	}
}

// FR-HR-06: the asset register records the employee who holds an asset;
// the offboarding checklist of a leaver lists the assets still held and
// "Company assets returned" cannot be ticked until they are returned.
func TestP5CloseOffboardingAssets(t *testing.T) {
	tm := closeProperty(t, "OA")
	sa := tm.sa
	e := prEmployee(t, tm, "Dodi", "TK/0", time.Now().AddDate(-1, 0, 0), "5500000", nil, "", false, nil)
	cat := idOf(sa.Must(201, "POST", "/api/v1/inventory/asset-categories", map[string]any{"code": "LT" + tm.sfx, "name": "Laptops " + tm.sfx,
		"assetClass": "it_equipment"}, "Idempotency-Key", newKey()))
	laptop := sa.Must(201, "POST", "/api/v1/inventory/assets", map[string]any{"code": "LT-" + tm.sfx, "name": "Laptop " + tm.sfx, "categoryId": cat,
		"custodianEmployeeId": e}, "Idempotency-Key", newKey()).JSON()
	if laptop["custodianEmployeeId"] != e {
		t.Fatalf("custodian: %v", laptop)
	}
	radio := idOf(sa.Must(201, "POST", "/api/v1/inventory/assets", map[string]any{"code": "HT-" + tm.sfx, "name": "Radio " + tm.sfx, "categoryId": cat},
		"Idempotency-Key", newKey()))
	sa.Must(200, "PATCH", "/api/v1/inventory/assets/"+radio, map[string]any{"custodianEmployeeId": e})
	// the custodian is an employee of the same property
	sa.Must(422, "PATCH", "/api/v1/inventory/assets/"+radio, map[string]any{"custodianEmployeeId": uuid.NewString()})
	var other string
	sysQueryRow(t, inst, `SELECT id::text FROM hris.employees WHERE property_id = $1 LIMIT 1`, []any{inst.Main}, &other)
	sa.Must(422, "PATCH", "/api/v1/inventory/assets/"+radio, map[string]any{"custodianEmployeeId": other})
	if held := sa.Must(200, "GET", "/api/v1/inventory/assets?filter[custodianEmployeeId]="+e, nil).Items(); len(held) != 2 {
		t.Fatalf("assets held: %v", held)
	}

	sa.Must(200, "POST", hrBase+"/employees/"+e+":terminate", map[string]any{"terminationType": "resigned", "effectiveDate": hrDay(5), "reason": "New job"})
	var item map[string]any
	for _, it := range sa.Must(200, "GET", hrBase+"/employees/"+e+"/offboarding", nil).Items() {
		if it["code"] == "return_assets" {
			item = it
		} else if it["assets"] != nil {
			t.Fatalf("assets on %v", it)
		}
	}
	if item == nil {
		t.Fatal("no return_assets item")
	}
	listed := item["assets"].([]any)
	if len(listed) != 2 || listed[0].(map[string]any)["code"] != "HT-"+tm.sfx || listed[1].(map[string]any)["code"] != "LT-"+tm.sfx {
		t.Fatalf("assets to return: %v", listed)
	}
	r := sa.Do("PATCH", hrBase+"/offboarding-items/"+str(item["id"]), map[string]any{"status": "done"})
	if r.Status != 409 || !strings.Contains(string(r.Body), "LT-"+tm.sfx) {
		t.Fatalf("ticked with assets held: %d %s", r.Status, r.Body)
	}
	// the laptop comes back, the radio is written off
	sa.Must(200, "PATCH", "/api/v1/inventory/assets/"+str(laptop["id"]), map[string]any{"custodianEmployeeId": nil})
	sa.Must(200, "PATCH", "/api/v1/inventory/assets/"+radio, map[string]any{"status": "out_of_service", "custodianEmployeeId": nil})
	for _, it := range sa.Must(200, "GET", hrBase+"/employees/"+e+"/offboarding", nil).Items() {
		if it["code"] == "return_assets" && it["assets"] != nil && len(it["assets"].([]any)) != 0 {
			t.Fatalf("returned assets still listed: %v", it)
		}
	}
	done := sa.Must(200, "PATCH", hrBase+"/offboarding-items/"+str(item["id"]), map[string]any{"status": "done", "notes": "Laptop and radio returned"}).JSON()
	if done["status"] != "done" {
		t.Fatalf("ticked: %v", done)
	}
}
