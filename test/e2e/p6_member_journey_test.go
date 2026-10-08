package e2e

import (
	"testing"
	"time"
)

// Member journey of the Member App (product owner, 7 Oct 2026): Book Tee
// Time with the fee estimate, paid online through the sandbox gateway,
// caddy preference per player honoured by the Caddy Master's auto-assign,
// My Guests, the resort quote, and Join Membership from the application to
// the activated membership — every payment mocked by the sandbox gateway.
func TestMemberJourney(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": map[string]any{"autoPay": false}})
	pub := anon(t, inst)
	dispatch := func(what string, done func() bool) {
		t.Helper()
		waitFor(t, 10*time.Second, what, func() bool {
			_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
			return done()
		})
	}

	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	m := login(t, inst, "member@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	day := clubDay(inst, 9, isWeekday)
	am := slotsOf(teeTimes(t, gm, course, day), "morning", 1)
	presentCaddies(t, gm, day)
	slot := str(am[10]["id"])

	// Review: the fee estimate per segment, with its components.
	q := m.Must(200, "GET", "/api/v1/member/golf/quote?teeTimeId="+slot, nil).JSON()
	segs := q["segments"].(map[string]any)
	if segs["member"] == nil || segs["guest_of_member"] == nil || q["caddy"] == nil {
		t.Fatalf("quote: %v", q)
	}
	eqAmount(t, "member estimate", segs["member"].(map[string]any)["total"], 640000)
	// Caddies to prefer, on duty that day.
	cl := m.Must(200, "GET", "/api/v1/member/golf/caddies?date="+day, nil).Items()
	if len(cl) < 3 || cl[0]["onDuty"] != true {
		t.Fatalf("caddies: %v", cl)
	}
	preferred := str(cl[len(cl)-1]["id"]) // last in the queue order: only the preference puts it first

	// Book: me + two guests, paid online (QRIS) through the sandbox gateway.
	h := m.Must(201, "POST", "/api/v1/member/golf/holds", map[string]any{"teeTimeId": slot, "players": 3}).JSON()
	b := m.Must(201, "POST", "/api/v1/member/golf/bookings", map[string]any{"bookingType": "member", "holdId": h["id"], "paymentMode": "prepaid", "paymentMethod": "qris",
		"players": []map[string]any{{"playerType": "member"}, {"playerType": "guest_of_member", "name": "Journey Guest One", "phone": "+628129997001", "hostIndex": 0},
			{"playerType": "guest_of_member", "name": "Journey Guest Two", "hostIndex": 0}}}, "Idempotency-Key", newKey()).JSON()
	bid := str(b["id"])
	pay, ok := b["payment"].(map[string]any)
	if b["status"] != "pending" || !ok || pay["status"] != "pending" || pay["integrationCode"] != "mock-payment" {
		t.Fatalf("prepaid booking waits for the gateway: %v %v", b["status"], b["payment"])
	}
	number := str(pay["number"])
	page := pub.Must(200, "GET", "/api/v1/public/sandbox-checkout/"+number, nil).JSON()
	if page["status"] != "pending" || page["qrString"] == nil {
		t.Fatalf("sandbox page: %v", page)
	}
	pub.Must(404, "GET", "/api/v1/public/sandbox-checkout/PAY-NOPE", nil)
	if done := pub.Must(200, "POST", "/api/v1/public/sandbox-checkout/"+number+":complete", map[string]any{"outcome": "paid"}).JSON(); done["status"] != "completed" {
		t.Fatalf("sandbox pay: %v", done)
	}
	pub.Must(409, "POST", "/api/v1/public/sandbox-checkout/"+number+":complete", map[string]any{"outcome": "paid"})
	dispatch("paid booking is confirmed", func() bool {
		return m.Must(200, "GET", "/api/v1/member/bookings/"+bid, nil).JSON()["status"] == "confirmed"
	})

	// Caddy per player: No Caddy is refused while the Caddy Policy makes a
	// caddy mandatory (demo club); preferred and request (any) are kept.
	b = m.Must(200, "GET", "/api/v1/member/bookings/"+bid, nil).JSON()
	pids := playerIDs(b)
	if q["caddy"].(map[string]any)["mandatory"] == true {
		m.Must(422, "PUT", "/api/v1/member/bookings/"+bid+"/caddy-requests", map[string]any{"players": []map[string]any{{"playerId": pids[1], "preference": "none"}}})
	}
	j := m.Must(200, "PUT", "/api/v1/member/bookings/"+bid+"/caddy-requests", map[string]any{"players": []map[string]any{
		{"playerId": pids[0], "preference": "preferred", "caddyId": preferred}, {"playerId": pids[1], "preference": "any"}, {"playerId": pids[2], "preference": "any"}}}).JSON()
	ps := j["players"].([]any)
	if ps[0].(map[string]any)["caddyPreference"] != "preferred" || ps[1].(map[string]any)["caddyPreference"] != "any" || ps[0].(map[string]any)["caddy"] != nil {
		t.Fatalf("caddy requests (requested ≠ assigned): %v", ps)
	}
	m.Must(422, "PUT", "/api/v1/member/bookings/"+bid+"/caddy-requests", map[string]any{"players": []map[string]any{{"playerId": pids[0], "preference": "preferred"}}})
	other := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "non_member", "channel": "walk_in", "teeTimeId": am[11]["id"],
		"contactName": "Not Mine", "contactPhone": "+628129997009", "players": []map[string]any{{"playerType": "non_member", "name": "Not Mine", "phone": "+628129997009"},
			{"playerType": "non_member", "name": "Not Mine Two", "phone": "+628129997008"}}}).JSON()
	m.Must(404, "PUT", "/api/v1/member/bookings/"+str(other["id"])+"/caddy-requests", map[string]any{"players": []map[string]any{}})

	// The Caddy Master's auto-assign serves the preferred caddy first.
	ca := gm.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": firstFlight(b), "auto": true}).Items()
	if len(ca) != 3 || str(ca[0]["caddyId"]) != preferred || str(ca[0]["playerIds"].([]any)[0]) != pids[0] {
		t.Fatalf("auto-assign with preferences: %v", ca)
	}
	j = m.Must(200, "GET", "/api/v1/member/bookings/"+bid+"/journey", nil).JSON()
	ps = j["players"].([]any)
	if c, _ := ps[0].(map[string]any)["caddy"].(map[string]any); c == nil || str(c["caddyId"]) != preferred || c["status"] != "assigned" {
		t.Fatalf("assigned caddy in the journey: %v", ps[0])
	}
	if ps[1].(map[string]any)["caddy"] == nil || ps[2].(map[string]any)["caddy"] == nil {
		t.Fatalf("journey caddies: %v", ps)
	}

	// My Guests from the booking (TBA guests excluded).
	gs := m.Must(200, "GET", "/api/v1/member/golf/guests", nil).Items()
	found := false
	for _, g := range gs {
		found = found || g["name"] == "Journey Guest One"
	}
	if !found {
		t.Fatalf("my guests: %v", gs)
	}

	// Resort: catalog and the quote of a bungalow stay (nothing is kept).
	ro := idOf(sa.Must(201, "POST", "/api/v1/commercial/rate-plans", map[string]any{"code": "MJ_RO", "name": "Journey Room Only", "serviceType": "bungalow", "minNights": 1}))
	rule(t, sa, map[string]any{"code": "HERON-RO", "name": "Heron Room Only", "serviceType": "bungalow", "itemRef": "HERON", "ratePlanId": ro,
		"unit": "night", "price": "900000", "revenueComponent": "bungalow"})
	heron := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "HERON", "name": "Heron", "maxAdults": 3, "bedrooms": 2}))
	sa.Must(201, "POST", "/api/v1/stay/bungalows", map[string]any{"code": "H-01", "name": "Heron 01", "typeId": heron, "view": "golf"})
	cat := m.Must(200, "GET", "/api/v1/member/stay-catalog", nil).JSON()
	if len(cat["bungalowTypes"].([]any)) == 0 || len(cat["ratePlans"].([]any)) == 0 {
		t.Fatalf("stay catalog: %v", cat)
	}
	arr := clubDateAgo(inst, 0, 0, -20)
	dep := clubDateAgo(inst, 0, 0, -22)
	var before, after int
	sysQueryRow(t, inst, `SELECT count(*) FROM stay.stays`, nil, &before)
	sq := m.Must(200, "POST", "/api/v1/member/stays:quote", map[string]any{"kind": "bungalow", "bungalowTypeId": heron, "arrivalDate": arr, "departureDate": dep,
		"ratePlan": "MJ_RO", "adults": 2}).JSON()
	eqAmount(t, "bungalow quote (2 nights)", sq["total"], 1800000)
	sysQueryRow(t, inst, `SELECT count(*) FROM stay.stays`, nil, &after)
	if after != before {
		t.Fatal("the quote must not keep a booking")
	}
	m.Must(200, "GET", "/api/v1/member/meeting-room-availability?date="+arr, nil)

	// Join Membership: apply (no account) → club approval → fee paid online → activated.
	admin := login(t, inst, "membership.admin@demo.oneclub.id", demoPassword)
	mgr := login(t, inst, "membership@demo.oneclub.id", demoPassword)
	types, packages := typeIDs(t, admin)
	app := pub.Must(201, "POST", "/api/v1/public/membership-applications", map[string]any{"propertyId": inst.Main, "guest": map[string]any{"name": "Joko Journey",
		"email": "joko.journey@example.test", "phone": "+628129997010"}, "typeId": types["IND"], "packageId": packages["IND-1Y"], "birthDate": "1985-02-03"}).JSON()
	no := str(app["applicationNo"])
	pub.Must(404, "POST", "/api/v1/public/membership-applications/"+no+":track", map[string]any{"email": "someone.else@example.test"})
	tr := pub.Must(200, "POST", "/api/v1/public/membership-applications/"+no+":track", map[string]any{"email": "JOKO.journey@example.test"}).JSON()
	if tr["status"] != "draft" && tr["status"] != "pending" {
		t.Fatalf("new application: %v", tr)
	}
	pub.Must(409, "POST", "/api/v1/public/membership-applications/"+no+":pay-online", map[string]any{"email": "joko.journey@example.test"})
	var aid string
	sysQueryRow(t, inst, `SELECT id::text FROM membership.applications WHERE number = $1`, []any{no}, &aid)
	sub := admin.Must(200, "POST", "/api/v1/membership/applications/"+aid+":submit", nil).JSON()
	mgr.Must(200, "POST", "/api/v1/platform/approvals/"+str(sub["approvalRequestId"])+":approve", map[string]any{})
	tr = pub.Must(201, "POST", "/api/v1/public/membership-applications/"+no+":pay-online", map[string]any{"email": "joko.journey@example.test", "method": "virtual_account"}).JSON()
	fp, _ := tr["payment"].(map[string]any)
	if tr["status"] != "approved" || fp == nil || fp["vaNumber"] == nil || dec(tr["outstanding"]).IsZero() {
		t.Fatalf("fee checkout: %v", tr)
	}
	pub.Must(200, "POST", "/api/v1/public/sandbox-checkout/"+str(fp["number"])+":complete", map[string]any{})
	dispatch("paid fee activates the membership", func() bool {
		tr = pub.Must(200, "POST", "/api/v1/public/membership-applications/"+no+":track", map[string]any{"email": "joko.journey@example.test"}).JSON()
		return tr["status"] == "completed"
	})
	if str(tr["memberNo"]) == "" {
		t.Fatalf("activated membership has a member number: %v", tr)
	}
}
