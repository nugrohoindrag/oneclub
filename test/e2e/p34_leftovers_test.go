package e2e

// Remaining P3 / P4 items closed after Release 4 (docs/p3-traceability.md
// and docs/p4-traceability.md open items): banquet food cost per event and
// the banquet cost of sales journal (PRD P4 §9.2), the migration CLI scopes
// (PRD P3 / P4 §11), and small test gaps (segment CSV export, commission
// section of the Accounting Export, accounting.journal_posted /
// payment_run_executed payloads, POS shift close with a cash variance).

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// p34Report runs a report and returns its rows.
func p34Report(t *testing.T, c *Client, code, query string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range c.Must(200, "GET", "/api/v1/reporting/reports/"+code+query, nil).JSON()["rows"].([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

// PRD P4 §9.2 Banquet Supply end to end through the real banquet flow: a
// menu item with a recipe (0.2 kg beef + 0.1 kg rice per portion) on a menu
// of the banquet kitchen outlet, BEO issued for 100 pax, event completed with
// 110 final pax → banquet.event_completed (K6) deducts 22 kg beef and 11 kg
// rice from the banquet kitchen at moving average (Rp125.000 / Rp14.000) →
// inventory.movement_posted (sourceType banquet) → the ledger posts rule
// DEF-INV-COGS-BQT: Dr 5140 Banquet cost of sales Rp2.904.000 / Cr 1151
// inventory Rp2.904.000. The Banquet Food Cost Report shows the event with
// theoretical cost (BEO scaled to 110 pax × standard cost Rp120.000 /
// Rp15.000 = Rp2.805.000) against the actual Rp2.904.000 (variance
// Rp99.000) and the actual food cost % of the F&B revenue; the detail
// report per ingredient; banquet roles read it (event detail Food Cost tab),
// other roles do not.
func TestP34LeftoversBanquetFoodCost(t *testing.T) {
	c := fixMainBook(t)
	sa := superAdmin(t, inst)
	bm := roleUser(t, inst, "banquet_manager")
	k := newInvKit(t, sa)
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "BQO" + k.sfx, "name": "Banquet Kitchen " + k.sfx,
		"outletType": "restaurant"}))
	kit := k.warehouse(t, sa, "BQK", map[string]any{"locationType": "kitchen", "outletId": outlet})
	beef := k.item(t, sa, "BEEF", k.kg, map[string]any{"standardCost": "120000"})
	rice := k.item(t, sa, "RICE", k.kg, map[string]any{"standardCost": "15000"})
	invReceive(t, kit, map[string]any{"itemId": beef, "quantity": "50", "unitCost": "125000"}, map[string]any{"itemId": rice, "quantity": "30", "unitCost": "14000"})
	rec := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "BRD" + k.sfx, "name": "Rendang Plate " + k.sfx}))
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": rec, "itemId": beef, "quantity": "0.2", "uomId": k.kg})
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": rec, "itemId": rice, "quantity": "0.1", "uomId": k.kg})

	menu := idOf(sa.Must(201, "POST", "/api/v1/banquet/menus", map[string]any{"code": "BFC" + k.sfx, "name": "Gala Buffet " + k.sfx, "menuType": "buffet",
		"outletId": outlet}))
	cat := idOf(sa.Must(201, "POST", "/api/v1/banquet/menu-categories", map[string]any{"menuId": menu, "name": "Main", "quota": 1}))
	dish := idOf(sa.Must(201, "POST", "/api/v1/banquet/menu-items", map[string]any{"menuId": menu, "categoryId": cat, "name": "Rendang " + k.sfx,
		"station": "buffet", "recipeId": rec}))
	typ := bqType(t, sa, "GL"+k.sfx, "banquet")
	hall := bqVenue(t, sa, map[string]any{"code": "GH" + k.sfx, "name": "Gala Hall " + k.sfx, "venueType": "function_room", "maxCapacity": 200})
	start := time.Now().Add(time.Hour)
	ev := sa.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": "Gala Dinner " + k.sfx, "eventTypeId": typ,
		"contactName": "PIC Gala " + k.sfx, "contactPhone": "+62813" + k.sfx[len(k.sfx)-6:],
		"start": rfc(start), "end": rfc(start.Add(3 * time.Hour)), "expectedPax": 100,
		"venues": []map[string]any{{"venueId": hall["id"], "functionName": "Dinner"}}}).JSON()
	eid, number := str(ev["id"]), str(ev["number"])
	sa.Must(200, "PUT", "/api/v1/banquet/events/"+eid+"/menu-selection", map[string]any{"menuId": menu, "itemIds": []string{dish}})

	// Definite, then the BEO issued for 100 pax: 20 kg beef, 10 kg rice.
	sa.Must(200, "POST", "/api/v1/banquet/events/"+eid+":make-definite", map[string]any{})
	b := sa.Must(201, "POST", "/api/v1/banquet/beos", map[string]any{"eventId": eid}).JSON()
	b = bm.Must(200, "POST", "/api/v1/banquet/beos/"+str(b["id"])+":issue", map[string]any{}, "If-Match", fmt.Sprintf(`"%v"`, b["rev"])).JSON()
	if b["status"] != "issued" || b["pax"].(float64) != 100 {
		t.Fatalf("issued BEO: %v", b)
	}
	req := map[string]string{}
	for _, r := range b["requirements"].([]any) {
		m := r.(map[string]any)
		req[str(m["itemId"])] = str(m["quantity"])
	}
	invEq(t, "BEO beef", req[beef], "20")
	invEq(t, "BEO rice", req[rice], "10")
	sa.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/charges", map[string]any{"kind": "additional_fnb", "description": "Gala buffet",
		"quantity": "110", "unitPrice": "250000"})

	// Completed with 110 pax → stock deducted (K6) → banquet cost of sales.
	bm.Must(200, "POST", "/api/v1/banquet/events/"+eid+":complete", map[string]any{"finalPax": 110})
	var mv map[string]any
	bqDispatch(t, "banquet consumption posted", func() bool {
		for _, m := range sa.Must(200, "GET", "/api/v1/inventory/stock-movements?sourceId="+eid, nil).Items() {
			if m["sourceType"] == "banquet" && m["movementType"] == "consumption" {
				mv = m
			}
		}
		return mv != nil
	})
	invEq(t, "beef left", invOnHand(t, sa, kit, beef), "28")
	invEq(t, "rice left", invOnHand(t, sa, kit, rice), "19")
	invEq(t, "consumption at moving average", mv["totalCost"], "-2904000")
	accDispatch(t)
	var posted string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.outbox WHERE event_type = 'inventory.movement_posted' AND payload->>'movementId' = $1`,
		[]any{str(mv["id"])}, &posted)
	var payload map[string]any
	if raw := invEvents(t, "inventory.movement_posted", str(mv["id"])); len(raw) != 1 || json.Unmarshal([]byte(raw[0]), &payload) != nil ||
		payload["sourceType"] != "banquet" || payload["sourceId"] != eid || payload["movementType"] != "consumption" {
		t.Fatalf("inventory.movement_posted of the banquet: %v", raw)
	}
	if st, n := accProcessed(t, mustUUID(posted)); st != "posted" || n != 1 {
		t.Fatalf("banquet consumption processed: %s %d", st, n)
	}
	j := accJournalOf(t, c, mustUUID(posted))
	ls := accLines(j)
	if len(ls) != 2 || !ls["5140"].Equal(decimal.NewFromInt(2_904_000)) || !ls["1151"].Equal(decimal.NewFromInt(-2_904_000)) {
		t.Fatalf("DEF-INV-COGS-BQT journal (Dr 5140 / Cr 1151 2.904.000): %v", ls)
	}
	if j["status"] != "posted" {
		t.Fatalf("banquet COGS journal: %v", j)
	}
	// accounting.journal_posted (contract §11): { journalId, number, journalDate, journalType, sourceType, sourceId, total, currency }.
	var jp map[string]any
	if raw := invEvents(t, "accounting.journal_posted", str(j["id"])); len(raw) != 1 || json.Unmarshal([]byte(raw[0]), &jp) != nil {
		t.Fatalf("accounting.journal_posted of the banquet COGS journal: %v", raw)
	}
	if jp["journalId"] != j["id"] || jp["number"] != j["number"] || jp["journalDate"] != j["journalDate"] || jp["journalType"] != "automatic" ||
		jp["sourceType"] != j["sourceType"] || jp["sourceType"] == nil || jp["total"] != "2904000" || jp["currency"] != "IDR" {
		t.Fatalf("accounting.journal_posted payload: %v (journal %v)", jp, j)
	}

	// Banquet Food Cost Report (per event and per ingredient).
	q := "?params[from]=" + start.In(clubLoc(inst)).Format("2006-01-02") + "&params[to]=" + start.In(clubLoc(inst)).Format("2006-01-02") + "&params[event]=" + number
	rows := p34Report(t, bm, "inventory.banquet_food_cost", q)
	if len(rows) != 1 || rows[0]["number"] != number || rows[0]["status"] != "completed" || rows[0]["pax"].(float64) != 110 {
		t.Fatalf("Banquet Food Cost Report: %v", rows)
	}
	r := rows[0]
	invEq(t, "theoretical (BEO × 110/100 × standard cost)", r["theoreticalCost"], "2805000")
	invEq(t, "actual (= COGS journal)", r["actualCost"], "2904000")
	invEq(t, "variance", r["variance"], "99000")
	invEq(t, "actual per pax", r["actualPerPax"], "26400")
	bill := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid+"/billing", nil).JSON()
	fnb := decimal.Zero
	for _, x := range bill["byComponent"].([]any) {
		if m := x.(map[string]any); m["revenueComponent"] == "banquet_fnb" || m["revenueComponent"] == "banquet_package" {
			fnb = fnb.Add(dec(m["net"]))
		}
	}
	if !fnb.IsPositive() {
		t.Fatalf("F&B revenue of the event: %v", bill["byComponent"])
	}
	invEq(t, "F&B revenue", r["fnbRevenue"], fnb.String())
	invEq(t, "actual food cost %", r["actualPercent"], decimal.NewFromInt(2_904_000).Div(fnb).Mul(decimal.NewFromInt(100)).Round(2).String())
	invEq(t, "theoretical food cost %", r["theoreticalPercent"], decimal.NewFromInt(2_805_000).Div(fnb).Mul(decimal.NewFromInt(100)).Round(2).String())
	lines := map[string]map[string]any{}
	for _, l := range p34Report(t, sa, "inventory.banquet_food_cost_lines", q) {
		lines[str(l["itemCode"])] = l
	}
	bl, rl := lines["BEEF"+k.sfx], lines["RICE"+k.sfx]
	if len(lines) != 2 || bl == nil || rl == nil {
		t.Fatalf("Banquet Food Cost Detail Report: %v", lines)
	}
	invEq(t, "beef theoretical qty", bl["theoreticalQuantity"], "22")
	invEq(t, "beef actual qty", bl["actualQuantity"], "22")
	invEq(t, "beef actual unit cost", bl["actualUnitCost"], "125000")
	invEq(t, "beef cost variance", bl["variance"], "110000")
	invEq(t, "rice theoretical cost", rl["theoreticalCost"], "165000")
	invEq(t, "rice cost variance", rl["variance"], "-11000")
	// Without the event filter the event is listed among the period's events.
	if !strings.Contains(fmt.Sprint(p34Report(t, sa, "inventory.banquet_food_cost", "?params[from]="+start.In(clubLoc(inst)).AddDate(0, 0, -1).Format("2006-01-02")+
		"&params[to]="+start.In(clubLoc(inst)).AddDate(0, 0, 1).Format("2006-01-02"))), number) {
		t.Fatal("event missing from the period report")
	}
	roleUser(t, inst, "golf_admin").Must(403, "GET", "/api/v1/reporting/reports/inventory.banquet_food_cost"+q, nil)
	roleUser(t, inst, "inventory_manager").Must(200, "GET", "/api/v1/reporting/reports/inventory.banquet_food_cost_lines"+q, nil)
}
