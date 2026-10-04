package e2e

import (
	"testing"
	"time"
)

// EP-26: non-member booking on the website — page data, availability with
// prices, court booking held until the online payment confirms it, voucher
// code, bungalow with online deposit, VIP Suite as a request, membership
// application, contact form, honeypot and customer dedup.
func TestP2Website(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": map[string]any{"autoPay": true}})
	pub := anon(t, inst)
	prop := inst.Main.String()

	fac := idOf(sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "WEB-TENNIS", "name": "Tennis", "facilityType": "tennis",
		"usageMode": "slot_booking", "priceItem": "WEB-TEN", "openingHours": allDay}))
	court := idOf(sa.Must(201, "POST", "/api/v1/sportclub/courts", map[string]any{"code": "WEB-TEN-1", "name": "Tennis 1", "facilityId": fac}))
	rule(t, sa, map[string]any{"code": "WEB-TEN", "name": "Tennis any time", "serviceType": "sport_court", "itemRef": "WEB-TEN", "unit": "slot", "unitMinutes": 60, "price": "150000"})
	page := pub.Must(200, "GET", "/api/v1/public/sport-club?propertyId="+prop, nil).JSON()
	if len(page["courts"].([]any)) == 0 {
		t.Fatalf("sport club page: %v", page)
	}
	day := time.Now().In(f.Loc).AddDate(0, 0, 3)
	av := pub.Must(200, "GET", "/api/v1/public/availability?propertyId="+prop+"&resourceType=sport_court&date="+day.Format("2006-01-02"), nil).JSON()
	if len(av["resources"].([]any)) == 0 {
		t.Fatalf("availability: %v", av)
	}
	if r := pub.Do("GET", "/api/v1/public/availability?propertyId="+prop+"&resourceType=tee_time", nil); r.Status != 400 {
		t.Fatalf("only website types: %s", r)
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, f.Loc)
	guest := map[string]any{"name": "Web Tamu", "phone": "+6281777000111", "email": "webtamu@site.test"}
	bk := pub.Must(201, "POST", "/api/v1/public/court-bookings", map[string]any{"propertyId": prop, "guest": guest, "courtId": court,
		"start": rfc(start), "end": rfc(start.Add(time.Hour)), "payMethod": "qris"}).JSON()
	co := bk["checkout"].(map[string]any)
	if co["total"] != "150000" || co["online"].(map[string]any)["status"] != "completed" {
		t.Fatalf("court checkout: %v", bk)
	}
	if _, err := inst.App.Dispatcher.DispatchPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	sysQueryRow(t, inst, `SELECT status FROM reservation.reservations WHERE code = $1`, []any{bk["reference"]}, &status)
	if status != "confirmed" {
		t.Fatalf("paid website booking is confirmed, got %s", status)
	}
	// Same visitor again → same customer (dedup); slot taken → 409.
	if r := pub.Do("POST", "/api/v1/public/court-bookings", map[string]any{"propertyId": prop, "guest": guest, "courtId": court,
		"start": rfc(start), "end": rfc(start.Add(time.Hour))}); r.Status != 409 {
		t.Fatalf("slot already booked: %s", r)
	}
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.customers WHERE phone LIKE '%81777000111' OR lower(email) = 'webtamu@site.test'`, nil, &n)
	if n != 1 {
		t.Fatalf("customer dedup: %d", n)
	}
	// Voucher code covers the whole booking: confirmed without online payment.
	gift := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "WEB-GIFT", "name": "Gift 300K", "kind": "value",
		"category": "gift", "unit": "rupiah", "faceValue": "300000", "price": "300000", "validityMonths": 12}))
	gv := sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": gift, "guestName": "Gift Buyer",
		"payment": map[string]any{"methodType": "cash"}}, "Idempotency-Key", newKey()).JSON()["vouchers"].([]any)[0].(map[string]any)
	vb := pub.Must(201, "POST", "/api/v1/public/court-bookings", map[string]any{"propertyId": prop, "guest": guest, "courtId": court,
		"start": rfc(start.Add(2 * time.Hour)), "end": rfc(start.Add(3 * time.Hour)), "voucherCode": gv["code"]}).JSON()
	if vb["status"] != "confirmed" || !dec(vb["checkout"].(map[string]any)["voucherPaid"]).Equal(dec("150000")) {
		t.Fatalf("voucher code booking: %v", vb)
	}

	// Bungalow with an online deposit; VIP Suite is a request.
	ro := idOf(sa.Must(201, "POST", "/api/v1/commercial/rate-plans", map[string]any{"code": "WEB_RO", "name": "Room Only (web)", "serviceType": "bungalow", "minNights": 1}))
	rule(t, sa, map[string]any{"code": "WEB-ALBA-RO", "name": "Albatross Room Only", "serviceType": "bungalow", "itemRef": "WEB-ALBA", "ratePlanId": ro,
		"unit": "night", "price": "1200000", "revenueComponent": "bungalow"})
	bt := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "WEB-ALBA", "name": "Albatross", "maxAdults": 4, "bedrooms": 2}))
	sa.Must(201, "POST", "/api/v1/stay/bungalows", map[string]any{"code": "WA-01", "name": "Albatross 01", "typeId": bt})
	if sp := pub.Must(200, "GET", "/api/v1/public/stay?propertyId="+prop, nil).JSON(); len(sp["bungalowTypes"].([]any)) == 0 {
		t.Fatalf("stay page: %v", sp)
	}
	arr := day.AddDate(0, 0, 10)
	if ba := pub.Must(200, "GET", "/api/v1/public/bungalow-availability?propertyId="+prop+"&from="+arr.Format("2006-01-02")+"&nights=2", nil).Items(); len(ba) == 0 {
		t.Fatal("bungalow availability")
	}
	st := pub.Must(201, "POST", "/api/v1/public/stays", map[string]any{"propertyId": prop, "guest": map[string]any{"name": "Keluarga Web", "email": "keluarga@site.test"},
		"kind": "bungalow", "bungalowTypeId": bt, "arrivalDate": arr.Format("2006-01-02"), "departureDate": arr.AddDate(0, 0, 2).Format("2006-01-02"),
		"ratePlan": "WEB_RO", "adults": 2, "payMethod": "virtual_account"}).JSON()
	if st["total"] != "2400000" || st["checkout"].(map[string]any)["online"] == nil {
		t.Fatalf("bungalow website booking: %v", st)
	}
	vip := idOf(sa.Must(201, "POST", "/api/v1/stay/vip-suites", map[string]any{"code": "WEB-VIP", "name": "VIP Web", "blockHours": 8}))
	rule(t, sa, map[string]any{"code": "WEB-VIP-ANY", "name": "VIP web block", "serviceType": "vip_suite", "itemRef": "WEB-VIP", "unit": "block", "unitMinutes": 480, "overtimePrice": "500000", "price": "5000000", "revenueComponent": "vip_suite"})
	vr := pub.Must(201, "POST", "/api/v1/public/stays", map[string]any{"propertyId": prop, "guest": guest, "kind": "vip_suite", "unitId": vip,
		"start": rfc(start.AddDate(0, 0, 5))}).JSON()
	if vr["checkout"] != nil {
		t.Fatalf("VIP suite is a request confirmed by staff: %v", vr)
	}

	// Membership page & application; contact form; honeypot.
	sp := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "WEB-SPORT", "name": "Sport Club (web)", "programKind": "sport_club"}))
	typ, _ := membershipType(t, sa, sp, "WEB-SC-IND", "Sport Individual", map[string]any{"annualFee": "3000000"})
	if ts := pub.Must(200, "GET", "/api/v1/public/membership-types?propertyId="+prop+"&programKind=sport_club", nil).Items(); len(ts) == 0 {
		t.Fatal("membership types")
	}
	app := pub.Must(201, "POST", "/api/v1/public/membership-applications", map[string]any{"propertyId": prop, "guest": map[string]any{"name": "Calon Member",
		"phone": "+6281999000222"}, "typeId": typ, "birthDate": "1990-05-05"}).JSON()
	if app["applicationNo"] == "" {
		t.Fatalf("membership application: %v", app)
	}
	pub.Must(202, "POST", "/api/v1/public/contact", map[string]any{"propertyId": prop, "guest": guest, "topic": "meeting", "message": "Quote for 80 pax seminar?"})
	before := 0
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.customers WHERE name = 'Spam Bot'`, nil, &before)
	pub.Must(202, "POST", "/api/v1/public/contact", map[string]any{"propertyId": prop, "guest": map[string]any{"name": "Spam Bot", "email": "bot@spam.test",
		"website": "http://spam"}, "message": "buy now"})
	after := 0
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.customers WHERE name = 'Spam Bot'`, nil, &after)
	if after != before {
		t.Fatal("honeypot submissions are dropped")
	}
	if r := pub.Do("POST", "/api/v1/public/contact", map[string]any{"propertyId": prop, "guest": map[string]any{"name": "No Contact"}, "message": "hi"}); r.Status != 422 {
		t.Fatalf("phone or e-mail required: %s", r)
	}
}
