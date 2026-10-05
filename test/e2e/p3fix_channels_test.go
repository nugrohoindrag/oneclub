package e2e

// PRD P3 gap fixes, package Channels & UI: Customer / Corporate 360 banquet
// sections, points as a POS tender with a customer and personal promo code,
// website inquiry consent, online payment of payment schedule lines.

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// FR-C360-01 / FR-C360-04: the Banquet / Event section of the Customer 360
// (placeholder "live") and of the Corporate 360 with contract value, money
// received and outstanding.
func TestP3FixChannelsCustomer360Banquet(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	taxes := bqTax(t, sa, "C3"+sfx)
	typ := bqType(t, sa, "C3"+sfx, "mice")
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "C3K" + sfx, "name": "PT Seminar " + sfx}))
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "C3P" + sfx, "name": "PIC Seminar " + sfx, "phone": "+62829" + sfx}))
	room := bqVenue(t, sa, map[string]any{"code": "C3R" + sfx, "name": "Opal Room " + sfx, "venueType": "function_room", "maxCapacity": 60})
	pkg := idOf(sa.Must(201, "POST", "/api/v1/banquet/packages", map[string]any{"code": "C3M" + sfx, "name": "Half Day Meeting " + sfx, "category": "mice",
		"pricingMethod": "per_pax", "price": "300000", "pricingMode": "nett", "taxCodes": taxes, "minPax": 10, "durationHours": 4}))
	start := bqDay(45, 9, 0)
	ev := sa.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": "Seminar " + sfx, "eventTypeId": typ, "customerId": cust,
		"corporateAccountId": corp, "start": rfc(start), "end": rfc(start.Add(4 * time.Hour)), "expectedPax": 20, "packageId": pkg,
		"venues": []map[string]any{{"venueId": room["id"], "functionName": "Seminar", "layout": ""}}}).JSON()
	contract := bilDec(ev["contractTotal"])
	if !contract.IsPositive() {
		t.Fatalf("event contract: %v", ev)
	}
	section := func(path string) map[string]any {
		t.Helper()
		v := sa.Must(200, "GET", path, nil).JSON()
		s, _ := v["sections"].(map[string]any)["banquet"].(map[string]any)
		if s == nil {
			t.Fatalf("%s has no banquet section: %v", path, v["sections"])
		}
		return s
	}
	eventOf := func(s map[string]any) map[string]any {
		t.Helper()
		for _, e := range asMaps(s["events"]) {
			if e["id"] == ev["id"] {
				return e
			}
		}
		t.Fatalf("event %v not in the banquet section: %v", ev["number"], s)
		return nil
	}
	c360 := sa.Must(200, "GET", "/api/v1/crm/customers/"+cust+"/360", nil).JSON()
	if c360["placeholders"].(map[string]any)["banquet"] != "live" {
		t.Fatalf("Customer 360 banquet placeholder: %v", c360["placeholders"])
	}
	e := eventOf(section("/api/v1/crm/customers/" + cust + "/360"))
	if e["status"] != ev["status"] || !bilDec(e["outstanding"]).Equal(contract) || e["companyName"] != "PT Seminar "+sfx {
		t.Fatalf("Customer 360 event: %v", e)
	}
	// The down payment received lowers the outstanding (Corporate 360 too).
	bl := sa.Must(201, "POST", "/api/v1/banquet/events/"+str(ev["id"])+"/payment-schedule", map[string]any{}).JSON()
	sc := bl["schedule"].(map[string]any)
	dp := asMaps(sc["lines"])[0]
	sa.Must(201, "POST", "/api/v1/billing/payment-schedules/"+str(sc["id"])+"/lines/"+str(dp["id"])+":pay",
		map[string]any{"methodType": "bank_transfer", "reference": "C3-" + sfx}, "Idempotency-Key", newKey())
	bqDispatch(t, "event definite on the DP", func() bool {
		return sa.Must(200, "GET", "/api/v1/banquet/events/"+str(ev["id"]), nil).JSON()["status"] == "definite"
	})
	corpSec := section("/api/v1/crm/corporate-accounts/" + corp + "/360")
	e = eventOf(corpSec)
	want := contract.Sub(bilDec(dp["amount"]))
	if !bilDec(e["received"]).Equal(bilDec(dp["amount"])) || !bilDec(e["outstanding"]).Equal(want) || !bilDec(corpSec["outstanding"]).Equal(want) {
		t.Fatalf("Corporate 360 event after the DP %v: %v / %v", dp["amount"], e, corpSec)
	}
	if !bilDec(corpSec["contractTotal"]).Equal(contract) || decimal.NewFromFloat(corpSec["upcoming"].(float64)).IntPart() < 1 {
		t.Fatalf("Corporate 360 banquet totals: %v", corpSec)
	}
}
