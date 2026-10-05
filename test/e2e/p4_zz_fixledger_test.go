package e2e

// P4 gap fixes of the ledger (package E): Stock Valuation = GL inventory
// (FR-VAL-05, EP-05 AC2, EP-28 AC, exit criterion #4) with a deliberate
// mismatch blocking the period close, opening stock outside the P&L
// (FR-MIG-P4-03/06), category ledger accounts (FR-INV-01/02), asset
// disposal (FR-AST-06), sales commission (FR-PST-02), the caddy fee
// settlement of a real golf round (K8 golf.caddy_settlement_approved,
// FR-REV-05), refund, folio close, invoice void and annual fee postings
// (FR-REL-P4-04). Fresh properties keep the amounts exact. The file sorts
// after the inventory tests, whose Stock Valuation Report spans every
// property the Super Admin sees.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/accounting"
	"oneclub/internal/billing"
	"oneclub/internal/kernel/dbtx"
)

// fixProperty creates a property with its book open since cutOver
// ("" = the first day of the current month).
func fixProperty(t *testing.T, prefix, cutOver string) (*Client, uuid.UUID) {
	t.Helper()
	sa := superAdmin(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	p := sa.Must(201, "POST", "/api/v1/platform/properties", map[string]any{"code": prefix + sfx, "name": "Ledger " + prefix + " " + sfx,
		"timezone": "Asia/Jakarta"}).JSON()
	c := *sa
	c.Property = mustUUID(str(p["id"]))
	if cutOver == "" {
		today := mustDate(invToday())
		cutOver = today.AddDate(0, 0, 1-today.Day()).Format("2006-01-02")
	}
	c.Must(200, "POST", accBase+"/book:load-template", map[string]any{"cutOverDate": cutOver})
	return &c, c.Property
}

// fixChecks returns the control reconciliation checks by code.
func fixChecks(t *testing.T, c *Client, asOf string) (bool, map[string]map[string]any) {
	t.Helper()
	rec := c.Must(200, "GET", accBase+"/reconciliation?asOf="+asOf, nil).JSON()
	out := map[string]map[string]any{}
	for _, x := range rec["checks"].([]any) {
		m := x.(map[string]any)
		out[str(m["code"])] = m
	}
	return rec["ok"] == true, out
}

// fixPL is the P&L balance (revenue, cost of sales, expenses) of a property.
func fixPL(t *testing.T, property uuid.UUID) decimal.Decimal {
	t.Helper()
	var s string
	sysQueryRow(t, inst, `SELECT coalesce(sum(l.debit - l.credit), 0)::text FROM accounting.journal_lines l JOIN accounting.accounts a ON a.id = l.account_id
		WHERE l.property_id = $1 AND a.account_type IN ('revenue', 'expense', 'cogs')`, []any{property}, &s)
	return dec(s)
}

// fixMovementEvent is the processed opening stock movement event of a property.
func fixOpeningStockEvent(t *testing.T, property uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	var eid uuid.UUID
	var st string
	sysQueryRow(t, inst, `SELECT event_id, status FROM accounting.processed_events WHERE property_id = $1 AND event_type = 'inventory.movement_posted'
		AND payload->>'sourceType' = 'opening_stock'`, []any{property}, &eid, &st)
	return eid, st
}

// fixPeriod returns the financial period of the current month.
func fixPeriod(t *testing.T, c *Client) map[string]any {
	t.Helper()
	today := mustDate(invToday())
	for _, p := range c.Must(200, "POST", accBase+"/periods:generate", map[string]any{"year": today.Year()}).Items() {
		if int(p["month"].(float64)) == int(today.Month()) {
			return c.Must(200, "GET", accBase+"/periods/"+str(p["id"]), nil).JSON()
		}
	}
	t.Fatal("period of the current month")
	return nil
}

func fixChecklistItem(t *testing.T, p map[string]any, code string) map[string]any {
	t.Helper()
	for _, x := range p["checklist"].([]any) {
		if m := x.(map[string]any); m["code"] == code {
			return m
		}
	}
	t.Fatalf("checklist item %s: %v", code, p["checklist"])
	return nil
}

// FR-VAL-05 / EP-05 AC2 / EP-28 AC / exit #4, FR-MIG-P4-03/06, FR-INV-01/02:
// the opening stock import books Dr inventory (per category account) / Cr
// opening balance equity — no P&L; waste and issues use the category
// accounts before the posting rules; Stock Valuation = GL inventory per
// inventory account; a deliberate difference fails the reconciliation and,
// once the Accounting Policies make it blocking, the period close.
func TestP4FixLedgerInventoryValuation(t *testing.T) {
	c, prop := fixProperty(t, "LV", "")
	today := invToday()
	sfx := invSfx()
	pcs := idOf(c.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": "PCS" + sfx, "name": "Pieces", "kind": "count"}))
	parent := idOf(c.Must(201, "POST", "/api/v1/inventory/categories", map[string]any{"code": "PRO" + sfx, "name": "Pro Shop " + sfx,
		"valuationMethod": "moving_average", "inventoryAccount": "1152", "cogsAccount": "5120", "wasteAccount": "6640", "varianceAccount": "5190"}))
	child := idOf(c.Must(201, "POST", "/api/v1/inventory/categories", map[string]any{"code": "BAL" + sfx, "name": "Golf Balls " + sfx, "parentId": parent}))
	plain := idOf(c.Must(201, "POST", "/api/v1/inventory/categories", map[string]any{"code": "SUP" + sfx, "name": "Supplies " + sfx}))
	ball := idOf(c.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "BALL" + sfx, "name": "Golf ball box", "baseUomId": pcs,
		"categoryId": child, "itemType": "retail", "standardCost": "1"}))
	soap := idOf(c.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "SOAP" + sfx, "name": "Soap", "baseUomId": pcs,
		"categoryId": plain, "itemType": "consumable", "standardCost": "1"}))
	wh := idOf(c.Must(201, "POST", "/api/v1/inventory/warehouses", map[string]any{"code": "LW" + sfx, "name": "Main store " + sfx, "locationType": "store"}))

	// Opening stock (cut-over opname) dated after the book cut-over: balance sheet only.
	csv := "warehouseCode,itemCode,quantity,unitCost\nLW" + sfx + ",BALL" + sfx + ",10,50000\nLW" + sfx + ",SOAP" + sfx + ",20,10000\n"
	res := c.Must(200, "POST", "/api/v1/inventory/opening-stock:import", map[string]any{"mode": "commit", "csv": csv, "filename": "cutover-" + sfx + ".csv",
		"businessDate": today}).JSON()
	if res["status"] != "completed" {
		t.Fatalf("opening stock import: %v", res)
	}
	invEq(t, "opening stock value", res["totalValue"], "700000")
	accDispatch(t)
	eid, st := fixOpeningStockEvent(t, prop)
	if st != "posted" {
		t.Fatalf("opening stock movement: %s", st)
	}
	ls := accLines(accJournalOf(t, c, eid))
	if !ls["1152"].Equal(decimal.NewFromInt(500_000)) || !ls["1151"].Equal(decimal.NewFromInt(200_000)) || !ls["3900"].Equal(decimal.NewFromInt(-700_000)) ||
		len(ls) != 3 {
		t.Fatalf("opening stock journal (Dr category inventory 1152 / default 1151, Cr opening balance equity): %v", ls)
	}
	accEq(t, "no P&L impact of the opening stock", fixPL(t, prop), 0)
	accEq(t, "stock variance untouched", accBal(t, prop, "5190", ""), 0)
	val := c.Must(200, "GET", "/api/v1/inventory/valuation", nil).JSON()
	invEq(t, "Stock Valuation", val["total"], "700000")
	ok, checks := fixChecks(t, c, today)
	if !ok || checks["inventory_valuation:1151"]["gl"] != "200000" || checks["inventory_valuation:1151"]["subledger"] != "200000" ||
		checks["inventory_valuation:1152"]["gl"] != "500000" || checks["inventory_valuation:1152"]["subledger"] != "500000" {
		t.Fatalf("TB = opname value per inventory account: %v", checks)
	}

	// Category accounts before the rules: waste of a ball box → waste account
	// of the parent category (6640); an issue of soap (no category account) →
	// the default cost of sales rule (5110).
	c.Must(201, "POST", "/api/v1/inventory/waste", map[string]any{"warehouseId": wh, "reason": "damaged", "lines": []map[string]any{{"itemId": ball, "quantity": "2"}}})
	c.Must(201, "POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": wh, "costCenter": "housekeeping", "reason": "Rooms",
		"lines": []map[string]any{{"itemId": soap, "quantity": "5"}}})
	accDispatch(t)
	accEq(t, "waste on the category waste account", accBal(t, prop, "6640", ""), 100_000)
	accEq(t, "default waste account untouched", accBal(t, prop, "5180", ""), 0)
	accEq(t, "ball boxes on the category inventory account", accBal(t, prop, "1152", ""), 400_000)
	accEq(t, "issue on the default rule", accBal(t, prop, "5110", ""), 50_000)
	accEq(t, "soap on the default inventory account", accBal(t, prop, "1151", ""), 150_000)
	// A goods receipt of ball boxes (procurement.goods_received, stocked by
	// inventory): GRNI against the category inventory account.
	gr := uuid.New()
	grEv := accPublish(t, prop, "procurement.goods_received", map[string]any{"goodsReceiptId": gr, "number": "GR-LV" + sfx, "purchaseOrderId": uuid.New(),
		"poNumber": "PO-LV" + sfx, "supplierId": uuid.Nil, "warehouseId": wh, "receivedDate": today, "currency": "IDR", "total": "220000",
		"lines": []map[string]any{{"itemId": ball, "quantity": "4", "uomId": pcs, "baseQuantity": "4", "unitCost": "55000", "baseUnitCost": "55000",
			"totalCost": "220000"}}})
	invHandled(t, grEv, "inventory.goods_receipt")
	if ls := accLines(accJournalOf(t, c, grEv)); !ls["1152"].Equal(decimal.NewFromInt(220_000)) || !ls["2113"].Equal(decimal.NewFromInt(-220_000)) || len(ls) != 2 {
		t.Fatalf("goods receipt on the category inventory account: %v", ls)
	}
	if ok, checks = fixChecks(t, c, today); !ok || checks["inventory_valuation:1152"]["gl"] != "620000" {
		t.Fatalf("Stock Valuation = GL after waste, issue and receipt: %v", checks)
	}

	// A deliberate difference: a manual journal on the inventory account.
	mj := c.Must(201, "POST", accBase+"/manual-journals", map[string]any{"journalDate": today, "journalType": "adjustment", "submit": true,
		"description": "Deliberate inventory difference", "lines": []map[string]any{{"accountId": accAccount(t, "1152"), "debit": "1000"},
			{"accountId": accAccount(t, "5190"), "credit": "1000"}}}).JSON()
	if mj["journalId"] == nil {
		t.Fatalf("manual journal: %v", mj)
	}
	ok, checks = fixChecks(t, c, today)
	if ok || checks["inventory_valuation:1152"]["ok"] != false || checks["inventory_valuation:1152"]["difference"] != "1000" ||
		checks["inventory_valuation:1151"]["ok"] != true {
		t.Fatalf("the reconciliation must show the difference of 1152: %v", checks)
	}
	per := fixPeriod(t, c)
	item := fixChecklistItem(t, per, "inventory_reconciled")
	if item["ok"] != false || item["blocking"] != false || !strings.Contains(str(item["detail"]), "1152 difference 1000") || per["canClose"] != true {
		t.Fatalf("checklist warning (not blocking by default): %v / canClose %v", item, per["canClose"])
	}
	accPolicy(t, superAdmin(t, inst), prop, "Accounting Policies", accounting.ClosingPolicyCode, map[string]any{"requireNoPostingExceptions": true,
		"requireInventoryReconciled": true})
	per = fixPeriod(t, c)
	if item = fixChecklistItem(t, per, "inventory_reconciled"); item["blocking"] != true || per["canClose"] != false {
		t.Fatalf("blocking checklist item: %v / canClose %v", item, per["canClose"])
	}
	r := c.Do("POST", accBase+"/periods/"+str(per["id"])+":close", map[string]any{})
	if r.Status != 409 || !strings.Contains(string(r.Body), "Stock valuation = GL inventory") {
		t.Fatalf("close with a stock difference: %s", r)
	}
	c.Must(200, "POST", accBase+"/journals/"+str(mj["journalId"])+":reverse", map[string]any{"reason": "difference explained"})
	if ok, checks = fixChecks(t, c, today); !ok {
		t.Fatalf("reconciled after the reversal: %v", checks)
	}
	per = fixPeriod(t, c)
	if item = fixChecklistItem(t, per, "inventory_reconciled"); item["ok"] != true || per["canClose"] != true {
		t.Fatalf("checklist after the reversal: %v / canClose %v", item, per["canClose"])
	}
}

// FR-MIG-P4-03 / FR-TRS-02: when Finance carried the inventory in the
// posted GL opening balances, the opening stock import posts nothing (no
// double count) and Stock Valuation = GL.
func TestP4FixLedgerOpeningStockCarried(t *testing.T) {
	c, prop := fixProperty(t, "LO", "")
	today := invToday()
	sfx := invSfx()
	ob := c.Must(201, "POST", accBase+"/opening-balances", map[string]any{"description": "Opening TB with inventory", "lines": []map[string]any{
		{"accountCode": "1151", "debit": "300000"}, {"accountCode": "3100", "credit": "300000"}}}).JSON()
	c.Must(200, "POST", accBase+"/opening-balances/"+str(ob["id"])+":post", map[string]any{})
	pcs := idOf(c.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": "PCS" + sfx, "name": "Pieces", "kind": "count"}))
	c.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "TOW" + sfx, "name": "Towel", "baseUomId": pcs, "itemType": "consumable", "standardCost": "1"})
	c.Must(201, "POST", "/api/v1/inventory/warehouses", map[string]any{"code": "LO" + sfx, "name": "Store " + sfx, "locationType": "store"})
	res := c.Must(200, "POST", "/api/v1/inventory/opening-stock:import", map[string]any{"mode": "commit", "businessDate": today,
		"csv": "warehouseCode,itemCode,quantity,unitCost\nLO" + sfx + ",TOW" + sfx + ",30,10000\n"}).JSON()
	if res["status"] != "completed" {
		t.Fatalf("opening stock import: %v", res)
	}
	accDispatch(t)
	if _, st := fixOpeningStockEvent(t, prop); st != "no_posting" {
		t.Fatalf("opening stock carried by the GL opening balances: %s", st)
	}
	accEq(t, "inventory = opening balance", accBal(t, prop, "1151", ""), 300_000)
	accEq(t, "opening balance equity untouched", accBal(t, prop, "3900", ""), 0)
	if ok, checks := fixChecks(t, c, today); !ok || checks["inventory_valuation:1151"]["difference"] != "0" {
		t.Fatalf("opening stock = GL opening inventory: %v", checks)
	}
}

// FR-AST-06 (+ FR-AST-05 category accounts): the depreciation run uses the
// accounts of the asset category; the approved disposal derecognises cost
// and accumulated depreciation with the proceeds and the gain or loss.
func TestP4FixLedgerAssetDisposal(t *testing.T) {
	today := mustDate(invToday())
	first := today.AddDate(0, 0, 1-today.Day())
	c, prop := fixProperty(t, "LA", first.AddDate(0, -2, 0).Format("2006-01-02"))
	sfx := invSfx()
	cat := idOf(c.Must(201, "POST", "/api/v1/inventory/asset-categories", map[string]any{"code": "CART" + sfx, "name": "Golf Carts " + sfx,
		"assetClass": "golf_cart", "depreciationMethod": "straight_line", "usefulLifeMonths": 60, "assetAccount": "1230", "accumulatedAccount": "1290",
		"expenseAccount": "6220"}))
	csv := "code,name,categoryId,acquisitionDate,acquisitionCost,openingAccumulated\nOC" + sfx + ",Old cart," + cat + ",2024-01-01,12000000,4000000\n"
	if r := c.OK("POST", "/api/v1/platform/imports", map[string]any{"entity": "inventory.asset", "mode": "commit", "filename": "assets.csv", "csv": csv}).JSON(); r["insertedRows"].(float64) != 1 {
		t.Fatalf("asset import: %v", r)
	}
	var old string
	sysQueryRow(t, inst, `SELECT id::text FROM inventory.assets WHERE code = $1`, []any{"OC" + sfx}, &old)
	period := first.AddDate(0, -1, 0).Format("2006-01")
	c.Must(201, "POST", "/api/v1/inventory/depreciation-runs", map[string]any{"period": period})
	accDispatch(t)
	accEq(t, "depreciation on the category expense account", accBal(t, prop, "6220", ""), 200_000)
	accEq(t, "default depreciation expense untouched", accBal(t, prop, "6510", ""), 0)
	accEq(t, "accumulated depreciation", accBal(t, prop, "1290", ""), -200_000)

	// FR-AST-06: the disposal goes through the Finance Manager's approval.
	c.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "asset_disposal", "name": "Asset disposal " + sfx,
		"propertyId": prop, "steps": []map[string]any{{"stepNo": 1, "name": "Finance Manager", "approverType": "user",
			"approverUserId": userID(t, "finance_manager")}}})
	fm := roleUser(t, inst, "finance_manager")
	fm.Property = prop
	dispose := func(aid, proceeds string) map[string]any {
		d := c.Must(200, "POST", "/api/v1/inventory/assets/"+aid+":dispose", map[string]any{"reason": "Replaced", "proceeds": proceeds}).JSON()
		if d["status"] != "active" || d["disposalApprovalId"] == nil {
			t.Fatalf("disposal waiting for approval: %v", d)
		}
		fm.Must(200, "POST", "/api/v1/platform/approvals/"+str(d["disposalApprovalId"])+":approve", map[string]any{"reason": "Write off"})
		accDispatch(t)
		var eid uuid.UUID
		sysQueryRow(t, inst, `SELECT event_id FROM accounting.processed_events WHERE event_type = 'inventory.asset_disposed' AND payload->>'assetId' = $1`,
			[]any{aid}, &eid)
		if st, n := accProcessed(t, eid); st != "posted" || n != 1 {
			t.Fatalf("disposal of %s: %s %d", aid, st, n)
		}
		return accJournalOf(t, c, eid)
	}
	// cost 12,000,000, accumulated 4,200,000 (book 7,800,000), proceeds 9,000,000 → gain 1,200,000
	j := dispose(old, "9000000")
	ls := accLines(j)
	if !ls["1290"].Equal(decimal.NewFromInt(4_200_000)) || !ls["1144"].Equal(decimal.NewFromInt(9_000_000)) || !ls["1230"].Equal(decimal.NewFromInt(-12_000_000)) ||
		!ls["7120"].Equal(decimal.NewFromInt(-1_200_000)) || len(ls) != 4 {
		t.Fatalf("disposal with a gain: %v", ls)
	}
	if j["journalDate"] != invToday() {
		t.Fatalf("disposal journal dated the disposal day: %v", j["journalDate"])
	}
	// cost 3,000,000 without depreciation, proceeds 250,000 → loss 2,750,000
	nw := idOf(c.Must(201, "POST", "/api/v1/inventory/assets", map[string]any{"code": "NC" + sfx, "name": "Broken cart", "categoryId": cat,
		"acquisitionDate": invToday(), "acquisitionCost": "3000000"}))
	ls = accLines(dispose(nw, "250000"))
	if !ls["1144"].Equal(decimal.NewFromInt(250_000)) || !ls["7220"].Equal(decimal.NewFromInt(2_750_000)) || !ls["1230"].Equal(decimal.NewFromInt(-3_000_000)) ||
		len(ls) != 3 {
		t.Fatalf("disposal with a loss: %v", ls)
	}
	accEq(t, "posting clearing nets out", accBal(t, prop, "1199", ""), 0)
}

// FR-PST-02: an approved sales commission statement accrues Dr 6130 / Cr
// 2173 once per statement; a statement whose clawbacks exceed the
// commission reverses the sides.
func TestP4FixLedgerCommission(t *testing.T) {
	c := accSetupMDR(t)
	sfx := invSfx()
	st, user := uuid.New(), uuid.New()
	payload := map[string]any{"statementId": st, "number": "CST-" + sfx, "userId": user, "period": "2026-09", "total": "1250000", "currency": "IDR"}
	e1 := accPublish(t, inst.MDR, "crm.commission_approved", payload)
	if s, n := accProcessed(t, e1); s != "posted" || n != 1 {
		t.Fatalf("commission: %s %d", s, n)
	}
	j := accJournalOf(t, c, e1)
	if ls := accLines(j); !ls["6130"].Equal(decimal.NewFromInt(1_250_000)) || !ls["2173"].Equal(decimal.NewFromInt(-1_250_000)) || len(ls) != 2 {
		t.Fatalf("commission journal: %v", ls)
	}
	for _, l := range j["lines"].([]any) {
		if m := l.(map[string]any); m["accountCode"] == "2173" && str(m["partnerId"]) != user.String() {
			t.Fatalf("commission payable per sales person: %v", m)
		}
	}
	if s, n := accProcessed(t, accPublish(t, inst.MDR, "crm.commission_approved", payload)); s != "no_posting" || n != 0 {
		t.Fatalf("the same statement posts once: %s %d", s, n)
	}
	claw := accPublish(t, inst.MDR, "crm.commission_approved", map[string]any{"statementId": uuid.New(), "number": "CST-C" + sfx, "userId": user,
		"period": "2026-10", "total": "-200000", "currency": "IDR"})
	if ls := accLines(accJournalOf(t, c, claw)); !ls["2173"].Equal(decimal.NewFromInt(200_000)) || !ls["6130"].Equal(decimal.NewFromInt(-200_000)) {
		t.Fatalf("net clawback statement: %v", ls)
	}
}

// fixMainBook makes sure MAIN has a book (TestP4AccountingGolfPerTransaction
// opens it at the business date when it runs first).
func fixMainBook(t *testing.T) *Client {
	t.Helper()
	c := accMain(t)
	if st := c.Must(200, "GET", accBase+"/book", nil).JSON(); st["book"] == nil {
		c.Must(200, "POST", accBase+"/book:load-template", map[string]any{"cutOverDate": accBusinessDay(t, c)})
	}
	return c
}

// FR-REV-05 / EP-21 AC3 / K8: a golf round with a caddy → the caddy fee is a
// liability when the folio is posted; the approved settlement publishes
// golf.caddy_settlement_approved; its payout releases the liability (Dr
// caddy fee liability, Cr bank, deductions to income).
func TestP4FixLedgerCaddySettlement(t *testing.T) {
	c := fixMainBook(t)
	sa := superAdmin(t, inst)
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	cashier := login(t, inst, "cashier@demo.oneclub.id", demoPassword)
	today := invToday()
	sfx := invSfx()
	day := clubDay(inst, 13, isWeekday)
	playDay, _ := time.ParseInLocation("2006-01-02", day, clubLoc(inst))
	slots := slotsOf(teeTimes(t, gm, demoCourse(t, inst), day), "afternoon", 10)
	slot := slots[len(slots)-1]
	sysExec(t, inst, `UPDATE golf.tee_times SET min_players = 1 WHERE id = $1`, mustUUID(str(slot["id"])))
	caddy := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "CL" + sfx[len(sfx)-5:], "name": "Caddy Ledger " + sfx, "gender": "female"}))
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": caddy, "at": rfc(at(playDay, 5, 30))})
	// post what other tests left pending on MAIN, so the deltas below are this round's
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": today})
	liab0, bank0, ded0 := accBal(t, inst.Main, "2171", ""), accBal(t, inst.Main, "1121", ""), accBal(t, inst.Main, "4830", "")
	bk := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "member", "channel": "back_office", "teeTimeId": slot["id"],
		"players": []map[string]any{{"playerType": "member", "memberNo": "D0001"}}}).JSON()
	payFolio(t, cashier, bk)
	fid := firstFlight(bk)
	ca := sa.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "assignments": []map[string]any{
		{"caddyId": caddy, "playerIds": []any{playerIDs(bk)[0]}}}}).Items()[0]
	fee := dec(ca["feeAmount"])
	if !fee.Equal(decimal.NewFromInt(150_000)) {
		t.Fatalf("caddy fee of the all-in rate: %v", ca["feeAmount"])
	}
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "auto": true})
	checkIn(t, sa, day, bk)
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":start", map[string]any{"at": rfc(time.Now().Add(-4 * time.Hour))})
	if fin := sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":complete", map[string]any{}).JSON(); fin["status"] != "completed" {
		t.Fatalf("round finished: %v", fin)
	}
	accDispatch(t)
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": today})
	accEq(t, "caddy fee liability of the round", accBal(t, inst.Main, "2171", "").Sub(liab0), -150_000)

	st := sa.Must(201, "POST", "/api/v1/golf/caddy-settlements", map[string]any{"caddyId": caddy, "periodStart": day, "periodEnd": day}).JSON()
	if st["status"] != "approved" || !dec(st["caddyFee"]).Equal(fee) {
		t.Fatalf("settlement (no workflow: approved at once): %v", st)
	}
	ded, total := dec(st["deductions"]), dec(st["total"])
	evs := invEvents(t, "golf.caddy_settlement_approved", str(st["id"]))
	if len(evs) != 1 || !strings.Contains(evs[0], `"settlementId": "`+str(st["id"])+`"`) || !strings.Contains(evs[0], `"total": "`+str(st["total"])+`"`) {
		t.Fatalf("golf.caddy_settlement_approved: %v", evs)
	}
	accDispatch(t)
	var eid uuid.UUID
	sysQueryRow(t, inst, `SELECT id FROM platform.outbox WHERE event_type = 'golf.caddy_settlement_approved' AND aggregate_id = $1`, []any{mustUUID(str(st["id"]))}, &eid)
	if s, _ := accProcessed(t, eid); s != "no_posting" {
		t.Fatalf("approval before the payout has no ledger effect: %s", s)
	}
	paid := sa.Must(200, "POST", "/api/v1/golf/caddy-settlements/"+str(st["id"])+":pay", map[string]any{"methodType": "bank_transfer", "reference": "TRF-" + sfx}).JSON()
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": today})
	per := map[string]decimal.Decimal{}
	ctx := dbtx.System(context.Background())
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.code, sum(l.debit - l.credit)::text FROM accounting.journal_lines l JOIN accounting.accounts a ON a.id = l.account_id
			WHERE l.source_type = 'billing.payout' AND l.source_id = $1 GROUP BY a.code`, str(paid["payoutId"]))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code, v string
			if err := rows.Scan(&code, &v); err != nil {
				return err
			}
			per[code] = dec(v)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if !per["2171"].Equal(total.Add(ded)) || !per["1121"].Equal(total.Neg()) || !per["4830"].Equal(ded.Neg()) {
		t.Fatalf("payout journal (Dr liability %s, Cr bank %s, Cr deductions %s): %v", total.Add(ded), total, ded, per)
	}
	accEq(t, "caddy fee liability released", accBal(t, inst.Main, "2171", "").Sub(liab0), 0)
	if !accBal(t, inst.Main, "1121", "").Sub(bank0).Equal(total.Neg()) || !accBal(t, inst.Main, "4830", "").Sub(ded0).Equal(ded.Neg()) {
		t.Fatalf("bank / deduction income after the payout")
	}
	for _, l := range sa.Must(200, "GET", "/api/v1/golf/caddy-liabilities", nil).Items() {
		if l["caddyId"] == caddy && !dec(l["liability"]).IsZero() {
			t.Fatalf("caddy sub-ledger after the payout: %v", l)
		}
	}
}

// FR-PST-02 / FR-REL-P4-04 on a property posting per transaction: payment
// and folio close (posted once), refund (billing.refund_processed), invoice
// issue and void, the annual membership fee deferral and its monthly
// recognition (membership.fee_paid / annual_fee_due), deferred revenue =
// sub-ledger.
func TestP4FixLedgerBillingPostings(t *testing.T) {
	c, prop := fixProperty(t, "LB", "")
	today := invToday()
	sfx := invSfx()
	accPolicy(t, superAdmin(t, inst), prop, "Accounting Configuration", accounting.ConfigCode, map[string]any{"postingModes": map[string]string{"other": "per_transaction"}})
	folio := func(name string, price string) string {
		f := idOf(c.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": name + " " + sfx}))
		if price != "" {
			c.Must(201, "POST", "/api/v1/billing/folios/"+f+"/lines", map[string]any{"chargeType": "other", "description": name, "unitPrice": price})
		}
		return f
	}
	pay := func(f, amount string) map[string]any {
		return c.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": f, "amount": amount, "methodType": "cash", "channel": "venue"}).JSON()
	}

	// Payment (per transaction) and folio close: one journal. The folio
	// belongs to the "other" business line (as a P1–P3 source would set).
	f1 := folio("Court hire", "300000")
	sysExec(t, inst, `UPDATE billing.folios SET business_line = 'other' WHERE id = $1`, mustUUID(f1))
	p1 := pay(f1, "300000")
	c.Must(200, "POST", "/api/v1/billing/folios/"+f1+":close", nil)
	accDispatch(t)
	js := c.Must(200, "GET", accBase+"/journals?filter[sourceId]="+str(p1["id"]), nil).Items()
	if len(js) != 1 {
		t.Fatalf("journal of the payment: %v", js)
	}
	if ls := accLines(c.Must(200, "GET", accBase+"/journals/"+str(js[0]["id"]), nil).JSON()); !ls["1111"].Equal(decimal.NewFromInt(300_000)) ||
		!ls["4890"].Equal(decimal.NewFromInt(-300_000)) {
		t.Fatalf("payment journal: %v", ls)
	}
	var closed uuid.UUID
	sysQueryRow(t, inst, `SELECT event_id FROM accounting.processed_events WHERE event_type = 'billing.folio_closed' AND payload->>'folioId' = $1`, []any{f1}, &closed)
	if s, n := accProcessed(t, closed); s != "no_posting" || n != 0 {
		t.Fatalf("folio close after the payment posts nothing more: %s %d", s, n)
	}

	// Refund (billing.refund_processed): Dr guest ledger / Cr cash.
	f2 := folio("Lesson package", "500000")
	p2 := pay(f2, "500000")
	rf := c.Must(201, "POST", "/api/v1/billing/refunds", map[string]any{"paymentId": p2["id"], "amount": "200000", "reason": "Lesson cancelled",
		"destination": "original_method"}).JSON()
	if rf["status"] != "completed" {
		t.Fatalf("refund: %v", rf)
	}
	accDispatch(t)
	var refundEv uuid.UUID
	sysQueryRow(t, inst, `SELECT event_id FROM accounting.processed_events WHERE event_type = 'billing.refund_processed' AND payload->>'refundId' = $1`,
		[]any{str(rf["id"])}, &refundEv)
	if ls := accLines(accJournalOf(t, c, refundEv)); !ls["1111"].Equal(decimal.NewFromInt(-200_000)) || !ls["1141"].Equal(decimal.NewFromInt(200_000)) ||
		len(ls) != 2 {
		t.Fatalf("refund journal: %v", ls)
	}

	// Invoice issued then voided: AR control back to zero.
	corp := idOf(c.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "LB" + sfx, "name": "PT Ledger " + sfx, "email": "ar" + sfx + "@lb.test"},
		"Idempotency-Key", newKey()))
	cf := str(c.Must(200, "POST", "/api/v1/billing/customer-folios", map[string]any{"corporateAccountId": corp}).JSON()["id"])
	f3 := folio("Corporate outing", "1000000")
	c.Must(200, "POST", "/api/v1/billing/customer-folios/"+cf+":merge", map[string]any{"folioIds": []string{f3}})
	inv := c.Must(201, "POST", "/api/v1/billing/invoices", map[string]any{"customerFolioId": cf, "corporateAccountId": corp, "termsDays": 30},
		"Idempotency-Key", newKey()).JSON()
	c.Must(200, "POST", "/api/v1/billing/invoices/"+str(inv["id"])+":issue", nil)
	accDispatch(t)
	accEq(t, "AR control after the issue", accBal(t, prop, "1142", ""), 1_000_000)
	c.Must(200, "POST", "/api/v1/billing/invoices/"+str(inv["id"])+":void", map[string]any{"reason": "Duplicate"})
	accDispatch(t)
	accEq(t, "AR control after the void", accBal(t, prop, "1142", ""), 0)
	var voidEv uuid.UUID
	sysQueryRow(t, inst, `SELECT event_id FROM accounting.processed_events WHERE event_type = 'billing.invoice_voided' AND property_id = $1`, []any{prop}, &voidEv)
	if ls := accLines(accJournalOf(t, c, voidEv)); !ls["1142"].Equal(decimal.NewFromInt(-1_000_000)) || !ls["1143"].Equal(decimal.NewFromInt(1_000_000)) {
		t.Fatalf("invoice void journal: %v", ls)
	}

	// Annual membership fee Rp12,000,000 paid (folio + deferral) and the first
	// month recognised (as membership records them in the billing sub-ledger).
	f4 := folio("Annual fee", "")
	accTaxedCharge(t, f4, "membership_annual_fee", "Annual fee "+sfx, 12_000_000, 0, nil)
	pay(f4, "12000000")
	fee := uuid.New()
	start := mustDate(today).AddDate(0, 0, 1-mustDate(today).Day()).Add(2 * time.Hour)
	ctx := dbtx.System(context.Background())
	recog := start.Add(time.Hour)
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if err := inst.App.Billing.PostDeferred(ctx, tx, billing.DeferredEntry{PropertyID: prop, LiabilityType: "annual_fee", RefType: "membership.fee", RefID: fee,
			EntryType: "deferral", Amount: decimal.NewFromInt(12_000_000), RevenueComponent: "membership_annual_fee", Description: "Annual fee paid",
			IdempotencyKey: "annual-fee-paid-" + fee.String(), OccurredAt: &start}); err != nil {
			return err
		}
		return inst.App.Billing.PostDeferred(ctx, tx, billing.DeferredEntry{PropertyID: prop, LiabilityType: "annual_fee", RefType: "membership.fee", RefID: fee,
			EntryType: "recognition", Amount: decimal.NewFromInt(1_000_000), RevenueComponent: "membership_annual_fee", OccurredAt: &recog,
			Description: "Annual fee recognised", IdempotencyKey: "annual-fee-" + fee.String() + "-" + start.Format("200601")})
	}); err != nil {
		t.Fatal(err)
	}
	paidEv := accPublish(t, prop, "membership.fee_paid", map[string]any{"feeId": fee, "membershipId": uuid.New(), "feeType": "annual", "amount": "12000000"})
	if s, n := accProcessed(t, paidEv); s != "posted" || n != 1 {
		t.Fatalf("annual fee posting: %s %d", s, n)
	}
	accEq(t, "deferred annual fees (12 months − 1 recognised)", accBal(t, prop, "2153", ""), -11_000_000)
	accEq(t, "annual fee revenue of the month", accBal(t, prop, "4620", ""), -1_000_000)
	accEq(t, "deferral against the deferred revenue clearing", accBal(t, prop, "2159", ""), 12_000_000)
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": today}) // the annual fee folio (daily summary)
	accEq(t, "deferred revenue clearing nets out with the folio", accBal(t, prop, "2159", ""), 0)
	if s, _ := accProcessed(t, accPublish(t, prop, "membership.annual_fee_due", map[string]any{"feeId": uuid.New()})); s != "no_posting" {
		t.Fatalf("annual fee due without new entries: %s", s)
	}
	accEq(t, "cash", accBal(t, prop, "1111", ""), 300_000+500_000-200_000+12_000_000)
	dfr := c.Must(200, "GET", accBase+"/deferred-revenue?to="+today, nil).JSON()
	for _, r := range dfr["rows"].([]any) {
		if m := r.(map[string]any); m["difference"] != "0" {
			t.Fatalf("deferred revenue %v: GL %v ≠ sub-ledger %v", m["type"], m["glBalance"], m["subledger"])
		}
	}
	if ok, checks := fixChecks(t, c, today); !ok {
		t.Fatalf("control reconciliation: %v", checks)
	}
}
