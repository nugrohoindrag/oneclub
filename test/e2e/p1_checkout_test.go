package e2e

// Golfer Check-out (PRD P1 FR-CHK-05, member journey "Payment / Settlement
// → Leave Club"): the day's extras (locker, halfway house F&B charged from
// the POS) land on the booking folio; the locker stays in use after the
// round and is released at the check-out, which settles and closes the folio.

import (
	"testing"
)

func TestP1GolferCheckOut(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	sa := superAdmin(t, inst)
	desk := roleUser(t, inst, "front_desk")
	course := demoCourse(t, inst)
	day := clubDay(inst, 33, isWeekday)
	slot := slotsOf(teeTimes(t, gm, course, day), "afternoon", 1)[6]
	presentCaddies(t, gm, day)

	b := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "non_member", "channel": "walk_in", "teeTimeId": slot["id"],
		"contactName": "Dewi Checkout", "contactPhone": "+628129990777",
		"players": []map[string]any{{"playerType": "non_member", "name": "Dewi Checkout", "phone": "+628129990777"},
			{"playerType": "non_member", "name": "Eko Checkout", "phone": "+628129990778"}}}).JSON()
	payFolio(t, desk, b)
	fid, pid := firstFlight(b), playerIDs(b)[0]
	gm.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "auto": true})
	gm.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "auto": true})
	gm.Must(200, "POST", "/api/v1/golf/check-ins", map[string]any{"method": "booking_code", "value": b["code"]})

	// bag drop and a daily locker with a fee on the booking folio
	gm.Must(201, "POST", "/api/v1/golf/bag-drops", map[string]any{"bookingPlayerId": pid, "tagNumber": "GCO-77"})
	var locker string
	for _, l := range gm.Must(200, "GET", "/api/v1/golf/lockers?limit=100", nil).Items() {
		if l["lockerStatus"] == "available" && l["status"] == "active" {
			locker = str(l["id"])
			break
		}
	}
	la := gm.Must(201, "POST", "/api/v1/golf/locker-assignments", map[string]any{"lockerId": locker, "assignmentType": "daily", "bookingPlayerId": pid, "fee": "25000"}).JSON()

	// the POS finds the golfer by bag tag and charges the halfway house order to the booking folio
	targets := sa.Must(200, "GET", "/api/v1/golf/charge-targets?date="+day+"&q=gco-77", nil).Items()
	if len(targets) != 1 || str(targets[0]["folioId"]) != str(b["folioId"]) || targets[0]["playerName"] != "Dewi Checkout" {
		t.Fatalf("charge targets by bag tag: %v", targets)
	}
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "GCO-HALF", "name": "Checkout Halfway", "outletType": "restaurant"}))
	drink := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "GCO-DRINK", "name": "Iced Tea", "productType": "beverage", "price": "35000"}))
	ord := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "servingDestination": "halfway_house", "destinationRef": "Halfway House",
		"lines": []map[string]any{{"productId": drink}}}).JSON()
	if c := sa.Must(200, "POST", "/api/v1/commercial/orders/"+str(ord["id"])+":charge", map[string]any{"folioId": targets[0]["folioId"]}).JSON(); c["status"] != "charged" {
		t.Fatalf("charge to the golfer: %v", c["status"])
	}

	// the round ends: the locker is still in use (shower, change)
	sq := "/api/v1/golf/starter-queue/"
	gm.Must(200, "POST", sq+fid+":tee-off", map[string]any{})
	if r := desk.Do("POST", "/api/v1/golf/bookings/"+str(b["id"])+":check-out", map[string]any{"methodType": "cash"}); r.Status != 409 {
		t.Fatalf("check-out during the round must fail: %s", r)
	}
	gm.Must(200, "POST", sq+fid+":finish", map[string]any{"holesPlayed": 18})
	active := false
	for _, a := range gm.Must(200, "GET", "/api/v1/golf/locker-assignments?filter[status]=active", nil).Items() {
		active = active || a["id"] == la["id"]
	}
	if !active {
		t.Fatal("the daily locker stays in use after the round")
	}

	// check-out desk: balance = locker + iced tea, locker and bag still open
	var entry map[string]any
	for _, e := range desk.Must(200, "GET", "/api/v1/golf/check-outs?date="+day, nil).Items() {
		if e["bookingId"] == b["id"] {
			entry = e
		}
	}
	if entry == nil || len(entry["lockers"].([]any)) != 1 || len(entry["bags"].([]any)) != 1 || entry["checkedOutAt"] != nil {
		t.Fatalf("check-out desk entry: %v", entry)
	}
	eqAmount(t, "balance at check-out", entry["balance"], 25000+35000)
	if r := desk.Do("POST", "/api/v1/golf/bookings/"+str(b["id"])+":check-out", map[string]any{}); r.Status != 409 {
		t.Fatalf("check-out with a balance needs a settlement: %s", r)
	}
	// the golf manager has no billing rights: no payment taken through the check-out
	if r := gm.Do("POST", "/api/v1/golf/bookings/"+str(b["id"])+":check-out", map[string]any{"methodType": "cash"}); r.Status != 403 {
		t.Fatalf("check-out without billing permissions: %s", r)
	}
	co := desk.Must(200, "POST", "/api/v1/golf/bookings/"+str(b["id"])+":check-out", map[string]any{"methodType": "card", "reference": "EDC-1"}).JSON()
	if co["checkedOutAt"] == nil {
		t.Fatalf("checked out: %v", co["checkedOutAt"])
	}
	eqAmount(t, "balance after check-out", co["folio"].(map[string]any)["balance"], 0)
	if f := sa.Must(200, "GET", "/api/v1/billing/folios/"+str(b["folioId"]), nil).JSON(); f["status"] != "closed" {
		t.Fatalf("folio closed at check-out: %v", f["status"])
	}
	for _, a := range gm.Must(200, "GET", "/api/v1/golf/locker-assignments?filter[status]=active", nil).Items() {
		if a["id"] == la["id"] {
			t.Fatal("the locker is released at check-out")
		}
	}
	for _, d := range gm.Must(200, "GET", "/api/v1/golf/bag-drops?date="+day, nil).Items() {
		if d["tagNumber"] == "GCO-77" && d["status"] != "collected" {
			t.Fatalf("bag handed back: %v", d["status"])
		}
	}
	if len(sa.Must(200, "GET", "/api/v1/golf/charge-targets?date="+day+"&q=gco-77", nil).Items()) != 0 {
		t.Fatal("a checked-out golfer is no charge target")
	}
	if r := desk.Do("POST", "/api/v1/golf/bookings/"+str(b["id"])+":check-out", map[string]any{}); r.Status != 409 {
		t.Fatalf("second check-out: %s", r)
	}
}
