package e2e

import (
	"testing"
)

// Demo feedback (9 Oct 2026): a website guest books and pays later, then
// pays part online from the manage link; the front desk books walk-ins
// without an account, checks them in before payment, moves the checked-in
// flight to a later tee time (first come first served, price kept),
// assigns the caddies, and takes the bill per player, in part and merged
// with another booking.
func TestFrontDeskBookingAndBill(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	fd := login(t, inst, "front.desk@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	day := clubDay(inst, 5, isWeekday)
	pm := slotsOf(teeTimes(t, gm, course, day), "afternoon", 10)
	if len(pm) < 12 {
		t.Fatalf("afternoon slots on tee 10: %d", len(pm))
	}
	presentCaddies(t, gm, day)

	// website: pay at the club, then part of it online from the manage link
	web := anon(t, inst)
	h := web.Must(201, "POST", "/api/v1/public/golf/holds", map[string]any{"teeTimeId": pm[8]["id"], "players": 2}).JSON()
	b := web.Must(201, "POST", "/api/v1/public/golf/bookings", map[string]any{"holdId": h["id"], "holdToken": h["holdToken"], "consent": true,
		"paymentMode": "pay_at_venue", "contact": map[string]any{"name": "Wulan Website", "phone": "+628129990061", "email": "wulan@example.test"},
		"players": []map[string]any{{"name": "Wira Website"}}}).JSON()
	if b["status"] != "confirmed" || b["payment"] != nil {
		t.Fatalf("pay later: confirmed without a payment: %v", b)
	}
	p := web.Must(200, "POST", "/api/v1/public/bookings/"+str(b["manageToken"])+":pay", map[string]any{"method": "qris", "amount": "100000"}).JSON()
	if pay, _ := p["payment"].(map[string]any); pay == nil || pay["status"] != "pending" {
		t.Fatalf("part payment online: %v", p)
	} else {
		eqAmount(t, "part paid online", pay["amount"], 100000)
	}

	// front desk: three walk-ins without an account, pay at the end
	walk := func(slot map[string]any, names ...string) map[string]any {
		var players []map[string]any
		for _, n := range names {
			players = append(players, map[string]any{"playerType": "non_member", "name": n})
		}
		return fd.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "walk_in", "channel": "walk_in", "teeTimeId": slot["id"],
			"contactName": names[0], "contactPhone": "+628129990062", "players": players}).JSON()
	}
	w := walk(pm[9], "Arif Walk-in", "Bayu Walk-in", "Candra Walk-in")
	if w["status"] != "confirmed" || w["paymentMode"] != "pay_at_venue" {
		t.Fatalf("walk-in pays at the end: %v / %v", w["status"], w["paymentMode"])
	}
	charges := w["folio"].(map[string]any)["charges"]
	fd.Must(200, "POST", "/api/v1/golf/check-ins", map[string]any{"method": "booking_code", "value": w["code"], "date": day})

	// FIFO: the checked-in flight moves to a later tee time, price kept
	moved := fd.Must(200, "POST", "/api/v1/golf/bookings/"+str(w["id"])+":reschedule", map[string]any{"teeTimeId": pm[10]["id"],
		"reason": "Late arrival", "keepPrice": true}).JSON()
	if moved["status"] != "checked_in" || moved["teeTimeId"] != pm[10]["id"] {
		t.Fatalf("FIFO move of a checked-in flight: %v %v", moved["status"], moved["teeTimeId"])
	}
	eqAmount(t, "price kept on the FIFO move", moved["folio"].(map[string]any)["charges"], int64(dec(charges).IntPart()))

	// the caddy is in the rate: the front desk assigns them
	if ca := fd.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": firstFlight(moved), "auto": true}).Items(); len(ca) != 3 {
		t.Fatalf("front desk caddy assignment: %v", ca)
	}

	// split bill: the first player pays their share
	bill := fd.Must(200, "GET", "/api/v1/golf/bookings/"+str(w["id"])+"/bill", nil).JSON()
	shares := bill["players"].([]any)
	first := shares[0].(map[string]any)
	after := fd.Must(200, "POST", "/api/v1/golf/bookings/"+str(w["id"])+"/bill:pay", map[string]any{"playerIds": []any{first["playerId"]},
		"methodType": "cash"}).JSON()
	if s := after["players"].([]any); s[0].(map[string]any)["due"] != "0" || s[1].(map[string]any)["due"] == "0" {
		t.Fatalf("split bill: only the first player has paid: %v", s)
	}
	// a part payment for the rest of the table
	after = fd.Must(200, "POST", "/api/v1/golf/bookings/"+str(w["id"])+"/bill:pay", map[string]any{"amount": "50000", "methodType": "card",
		"reference": "EDC-778899"}).JSON()
	eqAmount(t, "balance after the share and a part payment", after["balance"], dec(charges).IntPart()-dec(first["share"]).IntPart()-50000)

	// merged bill: one person pays this booking and another one
	w2 := walk(pm[11], "Dedi Walk-in", "Eko Walk-in")
	res := fd.Must(200, "POST", "/api/v1/golf/bills:pay-combined", map[string]any{"bookingIds": []any{w["id"], w2["id"]}, "methodType": "qris",
		"payerName": "Arif Walk-in"}).Items()
	for _, r := range res {
		if r["balance"] != "0" {
			t.Fatalf("merged bill settles every booking: %v", r)
		}
	}
}
