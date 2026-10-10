package e2e

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// Booking & operations of the MGCC bungalows (docs/requirement-booking-
// hotel-mgcc.md): the flyer prices from the one Stay price source (rate
// card = search = cart = bill, FR-H01, FR-H17), Long Stay only from 7
// nights, a Saturday night priced like a Monday, the website cart of two
// bungalows booked as a group with the deposit held on the mock gateway
// (FR-H35), paid → confirmed, a failed payment → Expired and the bungalow
// free again (FR-H36), a sold-out cart refused with its item, the
// restrictions per date with the supervisor override (FR-H65), Cek
// Booking, e-voucher and calendar (FR-H39), the guest cancellation under
// the rate plan (FR-H42), the occupancy invariant (FR-H03), the
// registration card with the keys (FR-H47–H49), the room move (FR-H53),
// the front office night audit (FR-H74) and Charge to Room refused after
// check-out (FR-H82).
func TestBungalowMGCC(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	pub := anon(t, inst)
	pid := inst.Main.String()
	loc := clubLoc(inst)
	day := func(d time.Time) string { return d.Format("2006-01-02") }
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	mon := nextWeekday(loc, time.Monday, 20)
	sat := nextWeekday(loc, time.Saturday, 20)

	// flyer types, Room Only and Long Stay (min 7 nights), breakfast add-on
	birdie := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "MG-BIRDIE", "name": "Birdie Room", "maxAdults": 2,
		"maxChildren": 1, "bedrooms": 1, "baseRate": "605000", "slug": "birdie", "sortOrder": 1,
		"amenities": []map[string]any{{"group": "general", "icon": "ac_unit", "label": "AC"}}}))
	alb := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "MG-ALB", "name": "Albatros Room", "maxAdults": 4, "maxChildren": 2,
		"bedrooms": 2, "baseRate": "1045000", "slug": "albatros", "sortOrder": 3}))
	unit := func(code, typ string) string {
		return idOf(sa.Must(201, "POST", "/api/v1/stay/bungalows", map[string]any{"code": code, "name": "Bungalow " + code, "typeId": typ, "view": "golf"}))
	}
	b1, b2 := unit("MG-B01", birdie), unit("MG-B02", birdie)
	unit("MG-A01", alb)
	sa.Must(201, "POST", "/api/v1/stay/rate-plans", map[string]any{"code": "MG-RO", "name": "Room Only", "planType": "standard", "derivation": "bar",
		"pricingMode": "nett", "freeCancelHours": 24, "cancelFeePercent": "50", "paymentPolicy": "deposit", "depositPercent": "50"})
	ls := idOf(sa.Must(201, "POST", "/api/v1/stay/rate-plans", map[string]any{"code": "MG-LS", "name": "Long Stay", "planType": "standard",
		"derivation": "fixed", "minNights": 7, "pricingMode": "nett", "bookingSources": []string{"website", "member_app", "front_desk", "phone"}}))
	sa.Must(201, "POST", "/api/v1/stay/rate-plan-prices", map[string]any{"ratePlanId": ls, "bungalowTypeId": birdie, "weekdayPrice": "550000"})
	sa.Must(201, "POST", "/api/v1/stay/rate-plan-prices", map[string]any{"ratePlanId": ls, "bungalowTypeId": alb, "weekdayPrice": "935000"})
	bf := idOf(sa.Must(201, "POST", "/api/v1/stay/addons", map[string]any{"code": "MG-BF", "name": "Breakfast", "category": "breakfast", "price": "75000",
		"unit": "per_person_night", "pricingMode": "nett", "taxable": false, "serviceCharge": false, "availability": "both"}))

	typeOf := func(s map[string]any, id string) map[string]any {
		for _, ty := range asMaps(s["types"]) {
			if ty["id"] == id {
				return ty
			}
		}
		t.Fatalf("type %s not in the search: %v", id, s["types"])
		return nil
	}
	rateOf := func(ty map[string]any, code string) map[string]any {
		for _, r := range asMaps(ty["rates"]) {
			if r["code"] == code {
				return r
			}
		}
		return nil
	}
	search := func(a time.Time, nights, adults int) map[string]any {
		return pub.Must(200, "GET", "/api/v1/public/stay/search?propertyId="+pid+"&checkin="+day(a)+"&checkout="+day(a.AddDate(0, 0, nights))+
			"&adults="+str(adults), nil).JSON()
	}

	// one price source: search, rate card and cart give the flyer prices
	s2 := search(mon, 2, 2)
	ro := rateOf(typeOf(s2, birdie), "MG-RO")
	if ro == nil || len(asMaps(ro["nights"])) != 2 || asMaps(ro["nights"])[0]["price"] != "605000" || asMaps(ro["nights"])[1]["price"] != "605000" {
		t.Fatalf("Birdie Room Only 2 nights: %v", ro)
	}
	if rateOf(typeOf(s2, birdie), "MG-LS") != nil {
		t.Fatal("Long Stay must not be offered for 2 nights")
	}
	if r := rateOf(typeOf(search(mon, 7, 2), birdie), "MG-LS"); r == nil || asMaps(r["nights"])[0]["price"] != "550000" || len(asMaps(r["nights"])) != 7 {
		t.Fatalf("Birdie Long Stay 7 nights: %v", r)
	}
	if r := rateOf(typeOf(search(sat, 1, 2), birdie), "MG-RO"); r == nil || asMaps(r["nights"])[0]["price"] != "605000" {
		t.Fatalf("a Saturday night costs like a Monday night: %v", r)
	}
	if ty := typeOf(search(mon, 2, 4), birdie); ty["fitsGuests"] != false || ty["capacityNote"] == nil {
		t.Fatalf("4 adults in a Birdie: %v", ty)
	}
	card := pub.Must(200, "GET", "/api/v1/public/stay/rate-card?propertyId="+pid, nil).Items()
	found := false
	for _, r := range card {
		if r["typeId"] == birdie && r["ratePlan"] == "MG-RO" {
			found = r["weekday"] == "605000" && r["weekend"] == "605000"
		}
	}
	if !found {
		t.Fatalf("rate card Birdie Room Only: %v", card)
	}
	cart := map[string]any{"propertyId": pid, "arrivalDate": day(mon), "departureDate": day(mon.AddDate(0, 0, 2)), "items": []map[string]any{
		{"bungalowTypeId": birdie, "ratePlan": "MG-RO", "adults": 2, "addons": []map[string]any{{"addonId": bf, "quantity": 1}}},
		{"bungalowTypeId": alb, "ratePlan": "MG-RO", "adults": 3, "occupantName": "Keluarga Budi"}}}
	q := pub.Must(200, "POST", "/api/v1/public/stays:quote", cart).JSON()
	items := asMaps(q["items"])
	if q["ok"] != true || len(items) != 2 || !dec(items[0]["roomTotal"]).Equal(dec(ro["total"])) {
		t.Fatalf("cart quote = search price: %v vs %v", q, ro["total"])
	}
	if !dec(items[0]["addonsTotal"]).Equal(dec("300000")) { // 2 persons × 2 nights × 75.000
		t.Fatalf("breakfast add-on: %v", items[0])
	}
	if !dec(q["total"]).Equal(dec(items[0]["total"]).Add(dec(items[1]["total"]))) || !dec(q["depositNow"]).IsPositive() {
		t.Fatalf("cart totals: %v", q)
	}

	// website booking: group, bungalows held, deposit on the mock gateway
	guest := map[string]any{"name": "Rudi Hartono", "phone": "+628129998801", "email": "rudi@mgcc.test"}
	book := func(c map[string]any) map[string]any {
		b := map[string]any{"guest": guest, "acceptTerms": true, "payMethod": "qris", "bookerTitle": "mr", "expectedArrival": "15:00"}
		for k, v := range c {
			b[k] = v
		}
		return pub.Must(201, "POST", "/api/v1/public/stay-bookings", b).JSON()
	}
	pub.Must(422, "POST", "/api/v1/public/stay-bookings", map[string]any{"propertyId": pid, "guest": guest, "arrivalDate": day(mon),
		"departureDate": day(mon.AddDate(0, 0, 2)), "items": cart["items"], "payMethod": "qris"}) // terms not accepted
	g := book(cart)
	code, token := str(g["code"]), str(g["token"])
	pay, _ := g["payment"].(map[string]any)
	if g["status"] != "awaiting_payment" || !strings.HasPrefix(code, "GRP") || len(asMaps(g["stays"])) != 2 || pay == nil ||
		!dec(pay["amount"]).Equal(dec(g["depositDue"])) || g["holdSeconds"].(float64) <= 0 {
		t.Fatalf("website group booking: %v", g)
	}
	if n := typeOf(search(mon, 2, 2), birdie)["available"].(float64); n != 1 {
		t.Fatalf("one Birdie held: %v left", n)
	}
	for _, n := range pay["numbers"].([]any) {
		pub.Must(200, "POST", "/api/v1/public/sandbox-checkout/"+str(n)+":complete", map[string]any{"outcome": "paid"})
	}
	waitFor(t, 15*time.Second, "the paid group confirmed", func() bool {
		return pub.Must(200, "GET", "/api/v1/public/stay-bookings/"+token+"?propertyId="+pid, nil).JSON()["status"] == "confirmed"
	})

	// a failed payment releases the bungalow; a sold-out cart is refused with its item
	one := book(map[string]any{"propertyId": pid, "arrivalDate": day(mon), "departureDate": day(mon.AddDate(0, 0, 2)),
		"items": []map[string]any{{"bungalowTypeId": birdie, "ratePlan": "MG-RO", "adults": 1}}})
	if ty := typeOf(search(mon, 2, 2), birdie); ty["full"] != true {
		t.Fatalf("Birdie full: %v", ty["available"])
	}
	full := pub.Do("POST", "/api/v1/public/stay-bookings", map[string]any{"propertyId": pid, "guest": guest, "acceptTerms": true, "payMethod": "qris",
		"arrivalDate": day(mon), "departureDate": day(mon.AddDate(0, 0, 2)), "items": []map[string]any{{"bungalowTypeId": birdie, "ratePlan": "MG-RO", "adults": 1}}})
	if full.Status != 409 || !strings.Contains(string(full.Body), "items[0]") {
		t.Fatalf("sold-out cart: %d %s", full.Status, full.Body)
	}
	ab := pub.Must(200, "POST", "/api/v1/public/stay-bookings/"+str(one["token"])+":abandon", map[string]any{"propertyId": pid}).JSON()
	if ab["status"] != "expired" {
		t.Fatalf("abandoned payment: %v", ab["status"])
	}
	if n := typeOf(search(mon, 2, 2), birdie)["available"].(float64); n != 1 {
		t.Fatalf("Birdie free again after the failed payment: %v", n)
	}

	// restrictions per date: website only, then every channel with the supervisor override
	sa.Must(201, "POST", "/api/v1/stay/rate-restrictions", map[string]any{"code": "MG-EVENT", "bungalowTypeId": alb, "startDate": day(mon.AddDate(0, 0, 7)),
		"endDate": day(mon.AddDate(0, 0, 7)), "closed": true, "bookingSources": []string{"website"}, "reason": "Acara klub"})
	if ty := typeOf(search(mon.AddDate(0, 0, 7), 1, 2), alb); ty["closedReason"] == nil {
		t.Fatalf("closed for the website: %v", ty)
	}
	pub.Must(409, "POST", "/api/v1/public/stay-bookings", map[string]any{"propertyId": pid, "guest": guest, "acceptTerms": true, "payMethod": "qris",
		"arrivalDate": day(mon.AddDate(0, 0, 7)), "departureDate": day(mon.AddDate(0, 0, 8)), "items": []map[string]any{{"bungalowTypeId": alb, "ratePlan": "MG-RO", "adults": 2}}})
	desk := map[string]any{"kind": "bungalow", "bungalowTypeId": alb, "arrivalDate": day(mon.AddDate(0, 0, 7)), "departureDate": day(mon.AddDate(0, 0, 8)),
		"ratePlan": "MG-RO", "adults": 2, "guest": map[string]any{"name": "Telepon Tamu", "phone": "+628129998802"}, "bookingSource": "phone"}
	sa.Must(201, "POST", "/api/v1/stay/stays", desk) // the restriction is for the website only
	sa.Must(201, "POST", "/api/v1/stay/rate-restrictions", map[string]any{"code": "MG-CTA", "startDate": day(mon.AddDate(0, 0, 9)),
		"endDate": day(mon.AddDate(0, 0, 9)), "closedToArrival": true})
	desk["arrivalDate"], desk["departureDate"] = day(mon.AddDate(0, 0, 9)), day(mon.AddDate(0, 0, 10))
	sa.Must(409, "POST", "/api/v1/stay/stays", desk)
	desk["overrideRestrictions"] = true
	sa.Must(422, "POST", "/api/v1/stay/stays", desk) // the reason is required
	desk["supervisorReason"] = "Tamu VIP, disetujui manajer"
	sa.Must(201, "POST", "/api/v1/stay/stays", desk)

	// Cek Booking, e-voucher, calendar, guest cancellation under the rate plan
	if lk := pub.Must(200, "POST", "/api/v1/public/stay-bookings:lookup", map[string]any{"propertyId": pid, "code": strings.ToLower(code),
		"contact": "08129998801"}).JSON(); lk["token"] != token {
		t.Fatalf("Cek Booking: %v", lk)
	}
	pub.Must(404, "POST", "/api/v1/public/stay-bookings:lookup", map[string]any{"propertyId": pid, "code": code, "contact": "someone@else.test"})
	if r := pub.Do("GET", "/api/v1/public/stay-bookings/"+token+"/e-voucher.pdf?propertyId="+pid, nil); r.Status != 200 || !strings.HasPrefix(string(r.Body), "%PDF") {
		t.Fatalf("e-voucher: %d", r.Status)
	}
	if r := pub.Do("GET", "/api/v1/public/stay-bookings/"+token+"/calendar.ics?propertyId="+pid, nil); r.Status != 200 || !strings.Contains(string(r.Body), "VEVENT") {
		t.Fatalf("calendar: %d", r.Status)
	}
	view := pub.Must(200, "GET", "/api/v1/public/stay-bookings/"+token+"?propertyId="+pid, nil).JSON()
	first := asMaps(view["stays"])[0]
	if first["canCancel"] != true || !dec(first["cancelFee"]).IsZero() {
		t.Fatalf("free cancellation 3 weeks ahead: %v", first)
	}
	cv := pub.Must(200, "POST", "/api/v1/public/stay-bookings/"+token+":cancel", map[string]any{"propertyId": pid, "stayIds": []string{str(first["id"])},
		"reason": "rencana berubah"}).JSON()
	if asMaps(cv["stays"])[0]["bookingStatus"] != "cancelled" {
		t.Fatalf("guest cancellation: %v", cv["stays"])
	}

	// occupancy never above 100 % (a bungalow created after the nights reported)
	rep := sa.Must(200, "GET", "/api/v1/stay/accommodation-report?from="+day(today.AddDate(0, 0, -40))+"&to="+day(today.AddDate(0, 0, -30)), nil).JSON()
	if dec(rep["occupancy"]).GreaterThan(dec("100")) {
		t.Fatalf("occupancy: %v", rep["occupancy"])
	}
	for _, n := range asMaps(rep["nights"]) {
		if n["occupied"].(float64) > n["available"].(float64) {
			t.Fatalf("sold more than available: %v", n)
		}
	}
	if d := sa.Must(200, "GET", "/api/v1/stay/dashboard", nil).JSON(); d["soldTonight"] == nil || d["inHouseNow"] == nil {
		t.Fatalf("overview cards: %v", d)
	}

	// front desk: registration card with the signature and the keys, room move, check-out with the keys
	in := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "unitId": b1, "arrivalDate": day(today.AddDate(0, 0, -1)),
		"departureDate": day(today.AddDate(0, 0, 1)), "ratePlan": "MG-RO", "adults": 2, "guest": map[string]any{"name": "Siti Front", "phone": "+628129998803"},
		"bookingSource": "walk_in"}).JSON()["stay"].(map[string]any)
	sid := str(in["id"])
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
	ci := sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":check-in", map[string]any{"idType": "passport", "idNumber": "X1234567", "nationality": "SG",
		"signature": png, "keysIssued": 2, "keyNumbers": "K-01, K-02"}).JSON()["stay"].(map[string]any)
	if ci["registeredAt"] == nil || ci["keysIssued"].(float64) != 2 || ci["nationality"] != "SG" {
		t.Fatalf("registration card: %v", ci)
	}
	if r := sa.Do("GET", "/api/v1/stay/stays/"+sid+"/registration-card.pdf", nil); r.Status != 200 || !strings.HasPrefix(string(r.Body), "%PDF") {
		t.Fatalf("registration card PDF: %d", r.Status)
	}
	mv := sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":move", map[string]any{"unitId": b2, "reason": "AC rusak"}).JSON()["stay"].(map[string]any)
	if mv["unitId"] != b2 {
		t.Fatalf("room move: %v", mv["unitId"])
	}
	for _, r := range sa.Must(200, "GET", "/api/v1/stay/room-status", nil).Items() {
		if r["id"] == b1 && r["hkStatus"] != "dirty" {
			t.Fatalf("the old bungalow after the move: %v", r["hkStatus"])
		}
	}
	due := str(sa.Must(200, "GET", "/api/v1/stay/stays/"+sid, nil).JSON()["stay"].(map[string]any)["totalDue"])
	sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": str(in["folioId"]), "methodType": "cash", "amount": due})
	end := at(today.AddDate(0, 0, 1), 12, 0)
	sa.Must(409, "POST", "/api/v1/stay/stays/"+sid+":check-out", map[string]any{"at": rfc(end), "keysReturned": 1})
	sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":check-out", map[string]any{"at": rfc(end), "keysReturned": 2})

	// Charge to Room is refused once the guest checked out
	if ih := sa.Must(200, "GET", "/api/v1/stay/in-house?q=MG-B0", nil).Items(); len(ih) != 0 {
		t.Fatalf("no in-house guest left: %v", ih)
	}
	sa.Must(409, "POST", "/api/v1/stay/stays/"+sid+":charge-order", map[string]any{"orderId": newKey()})

	// front office night audit: checklist, then the day closed once
	na := sa.Must(200, "GET", "/api/v1/stay/night-audit?date="+day(today.AddDate(0, 0, -2)), nil).JSON()
	if len(asMaps(na["checklist"])) < 4 || na["closed"] == true {
		t.Fatalf("night audit checklist: %v", na)
	}
	sa.Must(200, "POST", "/api/v1/stay/night-audit", map[string]any{"date": day(today.AddDate(0, 0, -2))})
	sa.Must(409, "POST", "/api/v1/stay/night-audit", map[string]any{"date": day(today.AddDate(0, 0, -2))})
	if fl := sa.Must(200, "GET", "/api/v1/stay/flash?date="+day(today.AddDate(0, 0, -2)), nil).JSON(); fl["frozen"] != true {
		t.Fatalf("flash frozen by the night audit: %v", fl)
	}
}

// The operations of the MGCC bungalows around a booking: a group with a
// rooming list and a combined bill booked at the desk, settled and checked
// in and out at once (FR-H64), the keys, an incident charged to the room
// (FR-H86), the shift handover (FR-H61), the identity photo (FR-H47), a
// free upgrade and a void by the supervisor (FR-H46, FR-H91), the website
// waitlist of a type and the payment continued (FR-H11, FR-H43), the
// Member App cart with its payment, failure and cancellation (FR-H87–H89)
// and a POS order charged to an in-house guest with the guest and the
// bungalow on every item (FR-H82).
func TestBungalowMGCCOperations(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	pub := anon(t, inst)
	pid := inst.Main.String()
	loc := clubLoc(inst)
	day := func(d time.Time) string { return d.Format("2006-01-02") }
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	mon := nextWeekday(loc, time.Monday, 30)

	eagle := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "MG-EAGLE", "name": "Eagle Room", "maxAdults": 2, "bedrooms": 1,
		"baseRate": "770000"}))
	vip := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "MG-VIPR", "name": "VIP Room", "maxAdults": 2, "bedrooms": 1,
		"baseRate": "1265000"}))
	unit := func(code, typ string) string {
		return idOf(sa.Must(201, "POST", "/api/v1/stay/bungalows", map[string]any{"code": code, "name": "Bungalow " + code, "typeId": typ, "view": "lake"}))
	}
	unit("MG-E01", eagle)
	unit("MG-E02", eagle)
	unit("MG-E03", eagle)
	v1 := unit("MG-V01", vip)
	sa.Must(201, "POST", "/api/v1/stay/rate-plans", map[string]any{"code": "MG-RO2", "name": "Room Only", "derivation": "bar", "pricingMode": "nett",
		"paymentPolicy": "deposit", "depositPercent": "50"})
	items := []map[string]any{{"bungalowTypeId": eagle, "ratePlan": "MG-RO2", "adults": 2, "occupantName": "Andi"},
		{"bungalowTypeId": eagle, "ratePlan": "MG-RO2", "adults": 1, "occupantName": "Budi"}}

	// a group at the desk: rooming list, combined bill, settled, in and out together
	sa.Must(200, "POST", "/api/v1/stay/stays:quote-cart", map[string]any{"arrivalDate": day(today.AddDate(0, 0, -1)), "departureDate": day(today.AddDate(0, 0, 1)),
		"items": items, "guest": map[string]any{"name": "PT Golf Outing"}})
	g := sa.Must(201, "POST", "/api/v1/stay/groups", map[string]any{"name": "PT Golf Outing", "guest": map[string]any{"name": "Rina Koordinator",
		"phone": "+628129998810"}, "folioMode": "combined", "arrivalDate": day(today.AddDate(0, 0, -1)), "departureDate": day(today.AddDate(0, 0, 1)),
		"items": items, "bookingSource": "phone"}, "Idempotency-Key", newKey()).JSON()
	gid := str(g["group"].(map[string]any)["id"])
	stays := asMaps(g["stays"])
	if len(stays) != 2 || stays[0]["groupNo"] == nil || stays[1]["occupantName"] != "Budi" {
		t.Fatalf("group with a rooming list: %v", g)
	}
	if r := sa.Must(200, "POST", "/api/v1/stay/groups/"+gid+":settle", map[string]any{"methodType": "cash"}).JSON(); len(r["done"].([]any)) != 2 {
		t.Fatalf("group deposit: %v", r)
	}
	if r := sa.Must(200, "POST", "/api/v1/stay/groups/"+gid+":check-in", map[string]any{"idType": "ktp", "idNumber": "3171000099990001"}).JSON(); len(r["done"].([]any)) != 2 {
		t.Fatalf("group check-in: %v", r)
	}
	s0 := str(stays[0]["id"])
	sa.Must(200, "POST", "/api/v1/stay/stays/"+s0+":keys", map[string]any{"keysReturned": 0})
	inc := sa.Must(201, "POST", "/api/v1/stay/incidents", map[string]any{"stayId": s0, "category": "damage", "severity": "high", "description": "Gelas pecah",
		"damageAmount": "50000", "chargeGuest": true}).JSON()
	if inc["bungalowCode"] == nil || inc["status"] != "open" {
		t.Fatalf("incident: %v", inc)
	}
	sa.Must(200, "POST", "/api/v1/stay/incidents/"+str(inc["id"])+":close", map[string]any{"actionTaken": "Gelas diganti, tamu dikenai biaya"})
	h := sa.Must(201, "POST", "/api/v1/stay/handover-notes", map[string]any{"category": "vip", "body": "Tamu VIP tiba pukul 20.00", "stayId": s0}).JSON()
	sa.Must(200, "POST", "/api/v1/stay/handover-notes/"+str(h["id"])+":done", map[string]any{})
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=")
	up, ctype := multipartBody(t, nil, "file", "ktp.png", string(png))
	if r := sa.Do("POST", "/api/v1/stay/stays/"+s0+"/identity-photo", up, "Content-Type", ctype); r.Status != 200 || r.JSON()["hasIdPhoto"] != true {
		t.Fatalf("identity photo: %d %s", r.Status, r.Body)
	}
	if r := sa.Do("GET", "/api/v1/stay/stays/"+s0+"/identity-photo", nil); r.Status != 200 {
		t.Fatalf("identity photo for the front desk: %d", r.Status)
	}
	// Charge to Room from the POS: the guest and the bungalow on every item
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "MG-SPIKE", "name": "Spike Bar", "outletType": "restaurant"}))
	coffee := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "MG-KOPI", "name": "Kopi Tubruk", "productType": "beverage", "price": "30000"}))
	if ih := sa.Must(200, "GET", "/api/v1/stay/in-house?q=MG-E0", nil).Items(); len(ih) != 2 {
		t.Fatalf("in-house guests for the POS: %v", ih)
	}
	ord := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "lines": []map[string]any{{"productId": coffee}}}).JSON()
	sa.Must(200, "POST", "/api/v1/stay/stays/"+s0+":charge-order", map[string]any{"orderId": ord["id"]})
	fo := sa.Must(200, "GET", "/api/v1/stay/stays/"+s0, nil).JSON()["folio"].(map[string]any)
	charged := false
	for _, l := range asMaps(fo["lines"]) {
		charged = charged || (strings.Contains(str(l["description"]), "Kopi Tubruk · Spike Bar · MG-E0") && strings.Contains(str(l["description"]), "Andi"))
	}
	if !charged {
		t.Fatalf("the POS item carries the outlet, bungalow and guest: %v", fo["lines"])
	}
	sa.Must(200, "POST", "/api/v1/stay/groups/"+gid+":settle", map[string]any{"methodType": "card"})
	if r := sa.Must(200, "POST", "/api/v1/stay/groups/"+gid+":check-out", map[string]any{"at": rfc(at(today.AddDate(0, 0, 1), 12, 0))}).JSON(); len(r["done"].([]any)) != 2 {
		t.Fatalf("group check-out: %v", r)
	}

	// a free upgrade, then a void by the supervisor
	r1 := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": eagle, "arrivalDate": day(mon),
		"departureDate": day(mon.AddDate(0, 0, 1)), "ratePlan": "MG-RO2", "guest": map[string]any{"name": "Upgrade Tamu"}}).JSON()["stay"].(map[string]any)
	up1 := sa.Must(200, "POST", "/api/v1/stay/stays/"+str(r1["id"])+":upgrade", map[string]any{"unitId": v1, "reason": "tamu setia"}).JSON()["stay"].(map[string]any)
	if up1["unitId"] != v1 || up1["totalDue"] != r1["totalDue"] {
		t.Fatalf("free upgrade keeps the price: %v → %v", r1["totalDue"], up1["totalDue"])
	}
	if v := sa.Must(200, "POST", "/api/v1/stay/stays/"+str(r1["id"])+":void", map[string]any{"reason": "salah input"}).JSON()["stay"].(map[string]any); v["status"] != "void" {
		t.Fatalf("void: %v", v["status"])
	}

	// the website: waitlist of a type, a booking whose payment is continued with another method
	guest := map[string]any{"name": "Web Tamu", "phone": "+628129998811", "email": "web@mgcc.test"}
	pub.Must(201, "POST", "/api/v1/public/stay-waitlist", map[string]any{"propertyId": pid, "guest": guest, "bungalowTypeId": vip, "arrivalDate": day(mon),
		"departureDate": day(mon.AddDate(0, 0, 2)), "adults": 2})
	b := pub.Must(201, "POST", "/api/v1/public/stay-bookings", map[string]any{"propertyId": pid, "guest": guest, "acceptTerms": true, "payMethod": "qris",
		"arrivalDate": day(mon), "departureDate": day(mon.AddDate(0, 0, 2)), "items": []map[string]any{{"bungalowTypeId": eagle, "ratePlan": "MG-RO2", "adults": 2}}}).JSON()
	cp := pub.Must(200, "POST", "/api/v1/public/stay-bookings/"+str(b["token"])+":pay", map[string]any{"propertyId": pid, "method": "virtual_account"}).JSON()
	if p, _ := cp["payment"].(map[string]any); p == nil || p["method"] != "virtual_account" || p["vaNumber"] == nil {
		t.Fatalf("payment continued: %v", cp["payment"])
	}
	my := pub.Must(200, "POST", "/api/v1/public/my-stay", map[string]any{"propertyId": pid, "token": b["token"]}).JSON()
	if my["bookingToken"] != b["token"] {
		t.Fatalf("My Stay from the link: %v", my["bookingToken"])
	}

	// Member App: search with the member rates, cart, payment continued, failed, cancelled
	m := login(t, inst, "member@demo.oneclub.id", demoPassword)
	m.Must(200, "GET", "/api/v1/member/stay/search?checkin="+day(mon.AddDate(0, 0, 3))+"&checkout="+day(mon.AddDate(0, 0, 4))+"&adults=2", nil)
	mc := map[string]any{"arrivalDate": day(mon.AddDate(0, 0, 3)), "departureDate": day(mon.AddDate(0, 0, 4)),
		"items": []map[string]any{{"bungalowTypeId": eagle, "ratePlan": "MG-RO2", "adults": 2}}}
	if q := m.Must(200, "POST", "/api/v1/member/stays:quote-cart", mc).JSON(); q["ok"] != true {
		t.Fatalf("member cart quote: %v", q)
	}
	mc["payMethod"] = "qris"
	mb := m.Must(201, "POST", "/api/v1/member/stay-bookings", mc, "Idempotency-Key", newKey()).JSON()
	tok := str(mb["token"])
	m.Must(200, "GET", "/api/v1/member/stay-bookings/"+tok, nil)
	m.Must(200, "POST", "/api/v1/member/stay-bookings/"+tok+":pay", map[string]any{"method": "card"})
	if ab := m.Must(200, "POST", "/api/v1/member/stay-bookings/"+tok+":abandon", map[string]any{}).JSON(); ab["status"] != "expired" {
		t.Fatalf("member payment failed: %v", ab["status"])
	}
	mb2 := m.Must(201, "POST", "/api/v1/member/stay-bookings", mc, "Idempotency-Key", newKey()).JSON()
	if cv := m.Must(200, "POST", "/api/v1/member/stay-bookings/"+str(mb2["token"])+":cancel", map[string]any{"reason": "batal"}).JSON(); cv["status"] != "expired" {
		t.Fatalf("member cancel before paying: %v", cv["status"])
	}
}
