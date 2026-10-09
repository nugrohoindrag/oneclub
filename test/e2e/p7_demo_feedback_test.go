package e2e

import (
	"testing"
	"time"
)

// Demo feedback 10 Oct 2026 #17: "Pay part now" from the Member App is a part
// payment of the bill — once the sandbox gateway settles it, the front desk
// bill, the per-player split and the member's booking count it as Paid.
func TestPartPaymentCountsAsPaid(t *testing.T) {
	setupP2(t)
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": map[string]any{"autoPay": false}})
	pub := anon(t, inst)
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	fd := login(t, inst, "front.desk@demo.oneclub.id", demoPassword)
	m := login(t, inst, "member@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	day := clubDay(inst, 11, isWeekday)
	am := slotsOf(teeTimes(t, gm, course, day), "morning", 1)

	h := m.Must(201, "POST", "/api/v1/member/golf/holds", map[string]any{"teeTimeId": am[14]["id"], "players": 2}).JSON()
	b := m.Must(201, "POST", "/api/v1/member/golf/bookings", map[string]any{"bookingType": "member", "holdId": h["id"], "paymentMode": "deposit",
		"depositAmount": "100000", "paymentMethod": "qris", "players": []map[string]any{{"playerType": "member"},
			{"playerType": "guest_of_member", "name": "Part Pay Guest", "hostIndex": 0}}}, "Idempotency-Key", newKey()).JSON()
	pay, ok := b["payment"].(map[string]any)
	if !ok || pay["purpose"] != "settlement" {
		t.Fatalf("pay part now is a part payment of the bill: %v", b["payment"])
	}
	eqAmount(t, "part paid online", pay["amount"], 100000)
	pub.Must(200, "POST", "/api/v1/public/sandbox-checkout/"+str(pay["number"])+":complete", map[string]any{"outcome": "paid"})
	bid := str(b["id"])
	waitFor(t, 10*time.Second, "part paid booking confirmed", func() bool {
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		return m.Must(200, "GET", "/api/v1/member/bookings/"+bid, nil).JSON()["status"] == "confirmed"
	})

	bill := fd.Must(200, "GET", "/api/v1/golf/bookings/"+bid+"/bill", nil).JSON()
	eqAmount(t, "front desk bill: paid", bill["paid"], 100000)
	eqAmount(t, "front desk bill: balance", bill["balance"], dec(bill["charges"]).IntPart()-100000)
	// the part payment goes to the booker's share first
	if first := bill["players"].([]any)[0].(map[string]any); dec(first["paid"]).IntPart() != 100000 {
		t.Fatalf("split per player: the booker's share holds the part payment: %v", bill["players"])
	}
	mine := m.Must(200, "GET", "/api/v1/member/bookings/"+bid, nil).JSON()
	if f, _ := mine["folio"].(map[string]any); f == nil || dec(f["payments"]).IntPart() != 100000 {
		t.Fatalf("Member App: paid = part payment: %v", mine["folio"])
	}
}

// Demo feedback 10 Oct 2026 #19: a checked-in player who no longer plays is
// removed before the tee-off with a reason; the caddy goes back to the queue
// and the green fee is voided. The last player cannot be removed.
func TestRemoveCheckedInPlayer(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	fd := login(t, inst, "front.desk@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	day := clubDay(inst, 12, isWeekday)
	pm := slotsOf(teeTimes(t, gm, course, day), "afternoon", 10)
	presentCaddies(t, gm, day)
	w := fd.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "walk_in", "channel": "walk_in", "teeTimeId": pm[14]["id"],
		"contactName": "Rama Walk-in", "contactPhone": "+628129990071", "players": []map[string]any{{"playerType": "non_member", "name": "Rama Walk-in"},
			{"playerType": "non_member", "name": "Sinta Walk-in"}}}).JSON()
	charges := dec(w["folio"].(map[string]any)["charges"]).IntPart()
	fd.Must(200, "POST", "/api/v1/golf/check-ins", map[string]any{"method": "booking_code", "value": w["code"], "date": day})
	ca := fd.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": firstFlight(w), "auto": true}).Items()
	if len(ca) != 2 {
		t.Fatalf("caddies: %v", ca)
	}
	var sinta string
	for _, p := range w["players"].([]any) {
		if p.(map[string]any)["name"] == "Sinta Walk-in" {
			sinta = str(p.(map[string]any)["id"])
		}
	}
	path := "/api/v1/golf/bookings/" + str(w["id"]) + "/players/" + sinta
	fd.Must(422, "DELETE", path, nil) // a checked-in player needs a reason
	b := fd.Must(200, "DELETE", path+"?reason=Sick", nil).JSON()
	for _, p := range b["players"].([]any) {
		if pp := p.(map[string]any); str(pp["id"]) == sinta && pp["status"] != "removed" {
			t.Fatalf("removed player: %v", pp)
		}
	}
	if left := dec(b["folio"].(map[string]any)["charges"]).IntPart(); left >= charges {
		t.Fatalf("green fee of the removed player voided: %d → %d", charges, left)
	}
	for _, a := range fd.Must(200, "GET", "/api/v1/golf/caddy-assignments?date="+day, nil).Items() {
		for _, pid := range a["playerIds"].([]any) {
			if str(pid) == sinta && a["status"] != "cancelled" {
				t.Fatalf("the caddy of the removed player goes back to the queue: %v", a)
			}
		}
	}
	var rama string
	for _, p := range b["players"].([]any) {
		if p.(map[string]any)["name"] == "Rama Walk-in" {
			rama = str(p.(map[string]any)["id"])
		}
	}
	fd.Must(409, "DELETE", "/api/v1/golf/bookings/"+str(w["id"])+"/players/"+rama+"?reason=Sick", nil)
}

// Demo feedback 10 Oct 2026 #37: a guest registers a non-member Member App
// account (code, password) and signs in; a member registers with the member
// no. only when a detail on file matches.
func TestPortalSignUp(t *testing.T) {
	pub := anon(t, inst)
	s := pub.Must(201, "POST", "/api/v1/public/portal-registrations", map[string]any{"propertyId": inst.Main, "kind": "guest", "name": "Sign Up Guest",
		"phone": "+628129990081", "email": "signup.guest@example.test"}).JSON()
	code := str(s["demoCode"])
	if len(code) != 6 {
		t.Fatalf("sign-up code on a test instance: %v", s)
	}
	pub.Must(422, "POST", "/api/v1/public/portal-registrations/"+str(s["id"])+":confirm", map[string]any{"code": "000000", "password": "Golf-Signup-2026!"})
	done := pub.Must(200, "POST", "/api/v1/public/portal-registrations/"+str(s["id"])+":confirm", map[string]any{"code": code, "password": "Golf-Signup-2026!"}).JSON()
	if done["status"] != "non_member" {
		t.Fatalf("guest account is a non-member: %v", done)
	}
	g := login(t, inst, "signup.guest@example.test", "Golf-Signup-2026!")
	me := g.Must(200, "GET", "/api/v1/auth/me", nil).JSON()
	if shells, _ := me["shells"].([]any); len(shells) == 0 || str(shells[0]) != "member" {
		t.Fatalf("the guest signs in to the Member App: %v", me["shells"])
	}
	g.Must(200, "GET", "/api/v1/member/golf/stats", nil) // linked to the customer profile
	pub.Must(409, "POST", "/api/v1/public/portal-registrations", map[string]any{"propertyId": inst.Main, "kind": "guest", "name": "Sign Up Guest",
		"email": "signup.guest@example.test"})
	// a member: the details must match the club's record
	pub.Must(422, "POST", "/api/v1/public/portal-registrations", map[string]any{"propertyId": inst.Main, "kind": "member", "memberNo": "D0001",
		"birthDate": "1901-01-01", "email": "nobody@example.test"})
	pub.Must(422, "POST", "/api/v1/public/portal-registrations", map[string]any{"propertyId": inst.Main, "kind": "member", "memberNo": "NOPE-0",
		"email": "nobody@example.test"})
}
