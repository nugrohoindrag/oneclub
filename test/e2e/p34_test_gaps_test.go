package e2e

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// Small test gaps of Release 3 / 4: FR-C360-03 segment CSV export (header,
// members, permission); FR-INT-P3-05 the commission section of the
// Accounting Export (adjustments Rp125.000 − Rp40.000 = Rp85.000 on a new
// property, earned and clawback 0); FR-PST-02 a POS shift closed with a
// cash variance (opening Rp500.000, counted Rp480.000 → short Rp20.000)
// publishes commercial.shift_closed and the ledger posts Dr 6910 cash over /
// short Rp20.000 / Cr 1111 cash Rp20.000.
func TestP34LeftoversTestGaps(t *testing.T) {
	sa := superAdmin(t, inst)
	mk := roleUser(t, inst, "marketing_staff")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)

	// Segment members export.
	a := customer(t, sa, "SGX"+sfx+"A", "Segment Export A "+sfx, map[string]any{"email": "sgxa" + sfx + "@seg.test", "phone": "+62817" + sfx})
	b := customer(t, sa, "SGX"+sfx+"B", "Segment Export B "+sfx, nil)
	seg := idOf(mk.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "SGX" + sfx, "name": "Export " + sfx, "segmentType": "static"}))
	mk.Must(200, "POST", "/api/v1/crm/segments/"+seg+"/members:add", map[string]any{"customerIds": []string{a, b}})
	r := mk.Must(200, "GET", "/api/v1/crm/segments/"+seg+"/members.csv", nil)
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") || !strings.Contains(r.Header.Get("Content-Disposition"), "segment-members.csv") {
		t.Fatalf("segment export headers: %v", r.Header)
	}
	recs, err := csv.NewReader(strings.NewReader(string(r.Body))).ReadAll()
	if err != nil || len(recs) != 3 || strings.Join(recs[0], ",") != "code,name,email,phone" {
		t.Fatalf("segment export: %v %s", err, r.Body)
	}
	got := map[string][]string{}
	for _, rec := range recs[1:] {
		got[rec[0]] = rec
	}
	if x := got["SGX"+sfx+"A"]; len(x) != 4 || x[1] != "Segment Export A "+sfx || x[2] != "sgxa"+sfx+"@seg.test" || x[3] != "+62817"+sfx {
		t.Fatalf("exported member A: %v", got)
	}
	if x := got["SGX"+sfx+"B"]; len(x) != 4 || x[2] != "" {
		t.Fatalf("exported member B: %v", got)
	}
	roleUser(t, inst, "golf_admin").Must(403, "GET", "/api/v1/crm/segments/"+seg+"/members.csv", nil)

	// Commission section of the Accounting Export on a new property.
	c, prop := fixProperty(t, "CMX", "")
	roleUser(t, inst, "sales_executive")
	sx := slsUserID(t, "role.sales_executive@matrix.test")
	c.Must(201, "POST", "/api/v1/crm/commission-adjustments", map[string]any{"userId": sx, "amount": "125000", "reason": "Shared deal bonus " + sfx})
	c.Must(201, "POST", "/api/v1/crm/commission-adjustments", map[string]any{"userId": sx, "amount": "-40000", "reason": "Correction " + sfx})
	exp := accountingExport(t, c)
	er, err := csv.NewReader(strings.NewReader(exp)).ReadAll()
	if err != nil || len(er) < 2 {
		t.Fatalf("accounting export: %v\n%s", err, exp)
	}
	col := map[string]int{}
	for i, h := range er[0] {
		col[h] = i
	}
	comm := map[string]string{}
	for _, rec := range er[1:] {
		if rec[col["section"]] == "commission" {
			comm[rec[col["code"]]] = rec[col["amount"]]
		}
	}
	if len(comm) != 3 {
		t.Fatalf("commission section of the Accounting Export: %v\n%s", comm, exp)
	}
	invEq(t, "commission adjustments", comm["adjustment"], "85000")
	invEq(t, "commission earned", comm["earned"], "0")
	invEq(t, "commission clawback", comm["clawback"], "0")

	// POS shift closed with a cash variance on the same property.
	outlet := idOf(c.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "CMXO" + sfx, "name": "Halfway House " + sfx, "outletType": "restaurant"}))
	sh := c.Must(201, "POST", "/api/v1/commercial/shifts:open", map[string]any{"outletId": outlet, "openingCash": "500000"}).JSON()
	z := c.Must(200, "POST", "/api/v1/commercial/shifts/"+str(sh["id"])+":close", map[string]any{"countedCash": "480000"}).JSON()
	invEq(t, "expected cash", z["expectedCash"], "500000")
	accDispatch(t)
	var closed map[string]any
	raw := invEvents(t, "commercial.shift_closed", str(sh["id"]))
	if len(raw) != 1 || json.Unmarshal([]byte(raw[0]), &closed) != nil || closed["variance"] != "-20000" || closed["countedCash"] != "480000" {
		t.Fatalf("commercial.shift_closed: %v", raw)
	}
	var eid string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.outbox WHERE event_type = 'commercial.shift_closed' AND aggregate_id = $1`,
		[]any{mustUUID(str(sh["id"]))}, &eid)
	if st, n := accProcessed(t, mustUUID(eid)); st != "posted" || n != 1 {
		t.Fatalf("shift variance processed: %s %d", st, n)
	}
	ls := accLines(accJournalOf(t, c, mustUUID(eid)))
	if len(ls) != 2 || !ls["6910"].Equal(decimal.NewFromInt(20_000)) || !ls["1111"].Equal(decimal.NewFromInt(-20_000)) {
		t.Fatalf("cash short journal (Dr 6910 / Cr 1111 20.000): %v", ls)
	}
	if !accBal(t, prop, "6910", "").Equal(decimal.NewFromInt(20_000)) {
		t.Fatal("cash over / short balance of the property")
	}
}
