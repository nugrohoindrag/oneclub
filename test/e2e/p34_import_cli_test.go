package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/app/dataimport"
)

var p34DateRe = regexp.MustCompile(`\{D([+-]\d+)\}`)

// p34Fixture reads a CSV fixture of the import CLI test and fills its
// placeholders ({SFX}, {TODAY}, {D±n}, {KG}, {CAT}).
func p34Fixture(t *testing.T, name string, vars map[string]string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "p34import", name))
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().In(clubLoc(inst))
	s := p34DateRe.ReplaceAllStringFunc(string(raw), func(m string) string {
		n, err := strconv.Atoi(p34DateRe.FindStringSubmatch(m)[1])
		if err != nil {
			t.Fatalf("placeholder %s: %v", m, err)
		}
		return today.AddDate(0, 0, n).Format("2006-01-02")
	})
	s = strings.ReplaceAll(s, "{TODAY}", today.Format("2006-01-02"))
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	if strings.Contains(s, "{") {
		t.Fatalf("unfilled placeholder in %s: %s", name, s)
	}
	return s
}

// p34Import runs `oneclub import <scope>` (dataimport.Run, the core of the
// CLI command) as the system actor against the test instance.
func p34Import(t *testing.T, property uuid.UUID, scope, entity, file string, commit bool, vars map[string]string,
	opts ...func(*dataimport.Request)) dataimport.Report {
	t.Helper()
	r := dataimport.Request{Scope: scope, Entity: entity, Property: property, CSV: p34Fixture(t, file, vars), Filename: file, Commit: commit}
	for _, o := range opts {
		o(&r)
	}
	rep, err := dataimport.Run(t.Context(), inst.App, r)
	if err != nil {
		t.Fatalf("oneclub import %s %s (commit %v): %v", scope, entity, commit, err)
	}
	if !strings.Contains(rep.Summary(), scope+" "+rep.Entity+" ("+rep.Mode+")") {
		t.Fatalf("summary: %s", rep.Summary())
	}
	return rep
}

func p34Counts(t *testing.T, what string, r dataimport.Report, rows, created, updated, existing, failed int) {
	t.Helper()
	if r.Rows != rows || r.Created != created || r.Updated != updated || r.Existing != existing || r.Failed != failed {
		t.Fatalf("%s (%s): want rows %d created %d updated %d existing %d failed %d, got\n%s", what, r.Mode, rows, created, updated, existing, failed,
			r.Summary())
	}
}

// PRD P3 / P4 §11 migration CLI: `oneclub import <scope>` wraps the API
// importers for every scope — sales leads, banquet events (DP = deposit
// liability), tournament history, corporate AR (control total), inventory
// warehouses / items / opening stock, procurement suppliers / supplier
// items / open POs, accounting chart of accounts and opening balances incl.
// an AP open item. Per scope: a dry run saves nothing, the commit saves, a
// committed re-run is idempotent, rejected rows are reported with their
// line, and the reconciliation figures are part of the report. The CSV
// fixtures are under testdata/p34import.
func TestP34LeftoversImportCLI(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	sfx := k.sfx[len(k.sfx)-6:]
	v := map[string]string{"SFX": sfx, "KG": k.kg, "CAT": k.cat}
	main := inst.Main
	if _, _, err := dataimport.Resolve("inventory", "nope"); err == nil {
		t.Fatal("unknown entity accepted")
	}
	if _, _, err := dataimport.Resolve("payroll", ""); err == nil {
		t.Fatal("unknown scope accepted")
	}
	if _, err := dataimport.PropertyByCode(t.Context(), inst.DB, "NOPE-"+sfx); err == nil {
		t.Fatal("unknown property accepted")
	}
	if p, err := dataimport.PropertyByCode(t.Context(), inst.DB, "main"); err != nil || p != main {
		t.Fatalf("property MAIN by code: %v %v", p, err)
	}

	// sales: 3 rows, the third without a name (line 4).
	p34Counts(t, "leads dry run", p34Import(t, main, "sales", "", "sales_leads.csv", false, v), 3, 2, 0, 0, 1)
	if l := sa.Must(200, "GET", "/api/v1/crm/leads?q=CLI+Bride+"+sfx, nil).Items(); len(l) != 0 {
		t.Fatalf("dry run saved leads: %v", l)
	}
	cm := p34Import(t, main, "sales", "leads", "sales_leads.csv", true, v)
	p34Counts(t, "leads", cm, 3, 2, 0, 0, 1)
	if cm.Issues[0].Row != 4 || cm.Reconciliation["opportunities"] != 0 {
		t.Fatalf("leads issues / opportunities: %s", cm.Summary())
	}
	p34Counts(t, "leads re-run", p34Import(t, main, "sales", "", "sales_leads.csv", true, v), 3, 0, 0, 2, 1)
	if l := sa.Must(200, "GET", "/api/v1/crm/leads?q=CLI+Bride+"+sfx, nil).Items(); len(l) != 1 {
		t.Fatalf("imported lead: %v", l)
	}

	// banquet: the row with pax "forty" is rejected (line 4); DP = deposit liability.
	p34Counts(t, "banquet dry run", p34Import(t, main, "banquet", "", "banquet_events.csv", false, v), 3, 2, 0, 0, 1)
	bq := p34Import(t, main, "banquet", "events", "banquet_events.csv", true, v)
	p34Counts(t, "banquet", bq, 3, 2, 0, 0, 1)
	if bq.Issues[0].Row != 4 || bq.Reconciliation["balanced"] != true || bq.Reconciliation["depositLiability"] != bq.Reconciliation["migratedDownPayment"] {
		t.Fatalf("banquet reconciliation: %s", bq.Summary())
	}
	p34Counts(t, "banquet re-run", p34Import(t, main, "banquet", "", "banquet_events.csv", true, v), 3, 0, 0, 2, 1)
	var dp string
	sysQueryRow(t, inst, `SELECT coalesce(sum(d.amount), 0)::text FROM banquet.events e JOIN billing.deposits d ON d.folio_id = e.folio_id
		WHERE e.legacy_ref = $1`, []any{"CLI-BQ-" + sfx + "-1"}, &dp)
	invEq(t, "DP held once", dp, "9000000")

	// tournament history: 1 tournament, 3 results, 2 champions; the re-run updates the same rows.
	th := p34Import(t, main, "tournament-history", "", "tournament_history.csv", false, v)
	if th.Rows != 3 || th.Failed != 0 || th.Reconciliation["results"] != 3 || th.Reconciliation["champions"] != 2 {
		t.Fatalf("tournament history dry run: %s", th.Summary())
	}
	p34Import(t, main, "tournament-history", "results", "tournament_history.csv", true, v)
	p34Import(t, main, "tournament-history", "", "tournament_history.csv", true, v)
	var results int
	sysQueryRow(t, inst, `SELECT count(*) FROM golf.tournament_results r JOIN golf.tournaments x ON x.id = r.tournament_id WHERE x.name = $1`,
		[]any{"CLI Championship " + sfx}, &results)
	if results != 3 {
		t.Fatalf("tournament results after the re-run: %d", results)
	}

	// inventory: warehouses and items (Master Data Import), opening stock.
	p34Counts(t, "warehouses", p34Import(t, main, "inventory", "warehouses", "inventory_warehouses.csv", true, v), 1, 1, 0, 0, 0)
	p34Counts(t, "items dry run", p34Import(t, main, "inventory", "", "inventory_items.csv", false, v), 2, 2, 0, 0, 0)
	if l := sa.Must(200, "GET", "/api/v1/inventory/items?q=CLI1"+sfx, nil).Items(); len(l) != 0 {
		t.Fatalf("dry run saved items: %v", l)
	}
	it := p34Import(t, main, "inventory", "items", "inventory_items.csv", true, v)
	p34Counts(t, "items", it, 2, 2, 0, 0, 0)
	p34Counts(t, "items re-run", p34Import(t, main, "inventory", "items", "inventory_items.csv", true, v), 2, 0, 2, 0, 0)
	var imports int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.imports WHERE id::text = $1 AND entity = 'inventory.item' AND inserted_rows = 2`,
		[]any{it.Reconciliation["importId"]}, &imports)
	if imports != 1 {
		t.Fatalf("the CLI import is in the Imports history: %v", it.Reconciliation)
	}
	ost := p34Import(t, main, "inventory", "opening-stock", "inventory_opening_stock.csv", false, v)
	if ost.Created != 2 || ost.Reconciliation["totalValue"] != "825000" || ost.Reconciliation["status"] != "valid" {
		t.Fatalf("opening stock dry run: %s", ost.Summary())
	}
	ost = p34Import(t, main, "inventory", "opening-stock", "inventory_opening_stock.csv", true, v)
	p34Counts(t, "opening stock", ost, 2, 2, 0, 0, 0)
	if ost.Reconciliation["status"] != "completed" || ost.Reconciliation["value.CLW"+sfx] != "825000" {
		t.Fatalf("opening stock: %s", ost.Summary())
	}
	p34Counts(t, "opening stock re-run", p34Import(t, main, "inventory", "opening-stock", "inventory_opening_stock.csv", true, v), 2, 0, 0, 2, 0)
	var onHand string
	sysQueryRow(t, inst, `SELECT coalesce(sum(b.quantity), 0)::text FROM inventory.stock_balances b JOIN inventory.items i ON i.id = b.item_id
		WHERE i.code = $1`, []any{"CLI1" + sfx}, &onHand)
	invEq(t, "opening stock posted once", onHand, "40")

	// procurement: suppliers, supplier items, the open PO (outstanding 6 × 14.500).
	p34Counts(t, "suppliers", p34Import(t, main, "procurement", "", "procurement_suppliers.csv", true, v), 1, 1, 0, 0, 0)
	p34Counts(t, "supplier items", p34Import(t, main, "procurement", "supplier-items", "procurement_supplier_items.csv", true, v), 1, 1, 0, 0, 0)
	po := p34Import(t, main, "procurement", "open-purchase-orders", "procurement_open_pos.csv", false, v)
	if po.Reconciliation["purchaseOrders"] != float64(1) || po.Reconciliation["outstandingValue"] != "87000" {
		t.Fatalf("open PO dry run: %s", po.Summary())
	}
	p34Counts(t, "open POs", p34Import(t, main, "procurement", "open-purchase-orders", "procurement_open_pos.csv", true, v), 1, 1, 0, 0, 0)
	p34Counts(t, "open POs re-run", p34Import(t, main, "procurement", "open-purchase-orders", "procurement_open_pos.csv", true, v), 1, 0, 0, 1, 0)
	if l := sa.Must(200, "GET", "/api/v1/procurement/purchase-orders?q=CLPO-"+sfx, nil).Items(); len(l) != 1 || l[0]["status"] != "partially_received" {
		t.Fatalf("migrated PO: %v", l)
	}

	// accounting: chart of accounts (instance level), then the opening
	// balances with an AP open item on a new property with its own book.
	p34Counts(t, "accounts", p34Import(t, main, "accounting", "accounts", "accounting_accounts.csv", true, v), 1, 1, 0, 0, 0)
	p34Counts(t, "accounts re-run", p34Import(t, main, "accounting", "accounts", "accounting_accounts.csv", true, v), 1, 0, 1, 0, 0)
	c, prop := fixProperty(t, "CLI", "")
	p34Import(t, prop, "procurement", "suppliers", "procurement_suppliers.csv", true, v)
	desc := func(r *dataimport.Request) { r.Description = "Excel Finance (CLI " + sfx + ")" }
	ob := p34Import(t, prop, "accounting", "opening-balances", "accounting_opening_balances.csv", false, v, desc)
	if ob.Rows != 4 || ob.Failed != 0 || ob.Reconciliation["balanced"] != true || ob.Reconciliation["totalDebit"] != "55000000" || ob.Reconciliation["batch"] != nil {
		t.Fatalf("opening balance dry run: %s", ob.Summary())
	}
	ob = p34Import(t, prop, "accounting", "opening-balances", "accounting_opening_balances.csv", true, v, desc)
	if ob.Created != 1 || ob.Reconciliation["batchStatus"] != "draft" {
		t.Fatalf("opening balance draft: %s", ob.Summary())
	}
	ob = p34Import(t, prop, "accounting", "opening-balances", "accounting_opening_balances.csv", true, v, desc)
	if ob.Updated != 1 || ob.Created != 0 {
		t.Fatalf("a re-run replaces the draft: %s", ob.Summary())
	}
	batches := c.Must(200, "GET", accBase+"/opening-balances", nil).Items()
	if len(batches) != 1 {
		t.Fatalf("one draft batch: %v", batches)
	}
	c.Must(200, "POST", accBase+"/opening-balances/"+str(batches[0]["id"])+":post", map[string]any{})
	ob = p34Import(t, prop, "accounting", "opening-balances", "accounting_opening_balances.csv", true, v, desc)
	if ob.Existing != 1 || ob.Created != 0 || ob.Reconciliation["batchStatus"] != "posted" || ob.Reconciliation["ok"] != true {
		t.Fatalf("a posted batch is not imported again: %s", ob.Summary())
	}
	if !accBal(t, prop, "CLI-BANK-"+sfx, "").Equal(decimal.NewFromInt(50_000_000)) || !accBal(t, prop, "2111", "").Equal(decimal.NewFromInt(-3_000_000)) {
		t.Fatal("opening journal of the imported batch")
	}

	// The migrated AP open item is paid by a payment run from the imported
	// bank account: accounting.payment_run_executed carries { runId, number,
	// paymentDate, total, currency, payments, journalId } (contract §11).
	today := time.Now().In(clubLoc(inst)).Format("2006-01-02")
	sups := c.Must(200, "GET", "/api/v1/procurement/suppliers?q=CLS"+sfx, nil).Items()
	if len(sups) != 1 {
		t.Fatalf("supplier of the property: %v", sups)
	}
	bank := idOf(c.Must(201, "POST", accBase+"/bank-accounts", map[string]any{"code": "CLIB-" + sfx, "name": "CLI Bank " + sfx, "kind": "bank",
		"glAccountId": accAccount(t, "CLI-BANK-"+sfx)}, "Idempotency-Key", newKey()))
	run := c.Must(201, "POST", accBase+"/payment-runs", map[string]any{"paymentDate": today, "bankAccountId": bank, "supplierId": sups[0]["id"],
		"dueBy": "2099-12-31"}).JSON()
	invEq(t, "payment run total = migrated AP", run["total"], "3000000")
	rid := str(run["id"])
	c.Must(200, "POST", accBase+"/payment-runs/"+rid+":submit", map[string]any{})
	ex := c.Must(200, "POST", accBase+"/payment-runs/"+rid+":execute", map[string]any{}).JSON()
	if ex["status"] != "executed" || ex["journalId"] == nil {
		t.Fatalf("executed run: %v", ex)
	}
	var pr map[string]any
	if raw := invEvents(t, "accounting.payment_run_executed", rid); len(raw) != 1 || json.Unmarshal([]byte(raw[0]), &pr) != nil {
		t.Fatalf("accounting.payment_run_executed: %v", raw)
	}
	if pr["runId"] != rid || pr["number"] != ex["number"] || pr["paymentDate"] != today || pr["total"] != "3000000" || pr["currency"] != "IDR" ||
		pr["payments"] != float64(1) || pr["journalId"] != ex["journalId"] {
		t.Fatalf("payment_run_executed payload: %v (run %v)", pr, ex)
	}
	pj := accLines(c.Must(200, "GET", accBase+"/journals/"+str(ex["journalId"]), nil).JSON())
	if !pj["2111"].Equal(decimal.NewFromInt(3_000_000)) || !pj["CLI-BANK-"+sfx].Equal(decimal.NewFromInt(-3_000_000)) {
		t.Fatalf("payment journal: %v", pj)
	}

	// corporate AR with the control total, on the new property after its opening
	// balances (the migrated open AR of MAIN stays as the other tests expect it).
	c.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "CLIAR" + sfx, "name": "PT CLI Debtor " + sfx})
	ctl := func(r *dataimport.Request) { r.ExpectedTotal = "20000000" }
	p34Counts(t, "AR dry run", p34Import(t, prop, "corporate-ar", "", "corporate_ar.csv", false, v, ctl), 2, 2, 0, 0, 0)
	ar := p34Import(t, prop, "corporate-ar", "invoices", "corporate_ar.csv", true, v, ctl)
	if ar.Created != 2 || ar.Reconciliation["reconciled"] != true || ar.Reconciliation["fileTotal"] != "20000000" {
		t.Fatalf("corporate AR: %s", ar.Summary())
	}
	p34Counts(t, "AR re-run", p34Import(t, prop, "corporate-ar", "", "corporate_ar.csv", true, v, ctl), 2, 0, 0, 2, 0)
}
