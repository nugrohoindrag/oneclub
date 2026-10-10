package e2e

import (
	"testing"
	"time"
)

// Court booking of the Sport Club desk (docs/requirement-booking-sportclub-
// mgcc.md): quote, payment, check-in by booking and by scanned code,
// rentals, overtime, end of play, no-show, discount, move and void by the
// supervisor, blocks, court staff reports, incidents, recurring bookings,
// the court policy, the website cart (quote, abandoned payment, Cek
// Booking) and the Member App (quote, guest ticket).
func TestSportClubCourtDesk(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	mc := login(t, inst, "member@demo.oneclub.id", demoPassword) // an active member of the demo seed (guest tickets need one)
	pub := anon(t, inst)
	prop := inst.Main.String()

	fac := idOf(sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "CB-FUTSAL", "name": "Futsal CB", "facilityType": "futsal",
		"usageMode": "slot_booking", "priceItem": "CB-FUTSAL", "openingHours": allDay}))
	courts := make([]string, 3)
	for i := range courts {
		courts[i] = idOf(sa.Must(201, "POST", "/api/v1/sportclub/courts", map[string]any{"code": "CB-FUTSAL-" + string(rune('1'+i)), "name": "Futsal CB " + string(rune('1'+i)),
			"facilityId": fac}))
	}
	rule(t, sa, map[string]any{"code": "CB-FUTSAL", "name": "Futsal CB any time", "serviceType": "sport_court", "itemRef": "CB-FUTSAL", "unit": "slot", "unitMinutes": 60,
		"price": "100000"})
	// the policy saved back as it is (a new version in force now)
	pol := sa.Must(200, "GET", "/api/v1/sportclub/court-policy", nil).JSON()
	sa.Must(200, "PUT", "/api/v1/sportclub/court-policy", pol)

	now := time.Now().In(f.Loc)
	hour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, f.Loc)
	line := func(court string, start time.Time, hours int) map[string]any {
		return map[string]any{"courtId": court, "start": rfc(start), "end": rfc(start.Add(time.Duration(hours) * time.Hour))}
	}
	if q := sa.Must(200, "POST", "/api/v1/sportclub/quote", map[string]any{"lines": []map[string]any{line(courts[0], hour, 1)}}).JSON(); q["lines"] == nil {
		t.Fatalf("desk quote: %v", q)
	}
	booking := func(b map[string]any) map[string]any {
		return b["booking"].(map[string]any)
	}
	firstLine := func(b map[string]any) string {
		return str(booking(b)["lines"].([]any)[0].(map[string]any)["id"])
	}

	// played now: paid, checked in, rentals and overtime charged, settled, finished
	b1 := sa.Must(201, "POST", "/api/v1/sportclub/bookings", map[string]any{"lines": []map[string]any{line(courts[0], hour, 1)},
		"guest": map[string]any{"name": "Tim Futsal CB", "phone": "+6281390001001"}, "via": "walk_in"}, "Idempotency-Key", newKey()).JSON()
	id1 := str(booking(b1)["id"])
	sa.Must(200, "POST", "/api/v1/sportclub/bookings/"+id1+":pay", map[string]any{"methodType": "cash"}, "Idempotency-Key", newKey())
	if r := sa.Must(200, "POST", "/api/v1/sportclub/bookings/"+id1+":check-in", map[string]any{}).JSON(); r["result"] != "checked_in" {
		t.Fatalf("check-in: %v", r)
	}
	sa.Must(200, "POST", "/api/v1/sportclub/bookings/"+id1+":extras", map[string]any{"items": []map[string]any{{"name": "Rompi", "price": "10000", "quantity": 2}}})
	ext := sa.Must(200, "POST", "/api/v1/sportclub/bookings/"+id1+":extend", map[string]any{"lineId": firstLine(b1), "hours": 1}).JSON()
	if n := len(booking(ext)["lines"].([]any)); n < 1 {
		t.Fatalf("overtime: %v", ext)
	}
	sa.Must(200, "POST", "/api/v1/sportclub/bookings/"+id1+":pay", map[string]any{"methodType": "qris", "reference": "QR-CB"}, "Idempotency-Key", newKey())
	sa.Must(200, "POST", "/api/v1/sportclub/bookings/"+id1+":complete", map[string]any{})

	// paid at booking, checked in by the scanned code
	b2 := sa.Must(201, "POST", "/api/v1/sportclub/bookings", map[string]any{"lines": []map[string]any{line(courts[1], hour, 1)},
		"guest": map[string]any{"name": "Scan CB", "phone": "+6281390001002"}, "payment": map[string]any{"methodType": "cash"}}, "Idempotency-Key", newKey()).JSON()
	if r := sa.Must(200, "POST", "/api/v1/sportclub/check-in:scan", map[string]any{"code": booking(b2)["code"]}).JSON(); r["result"] != "checked_in" {
		t.Fatalf("scan check-in: %v", r)
	}

	// started without the players: no-show
	b3 := sa.Must(201, "POST", "/api/v1/sportclub/bookings", map[string]any{"lines": []map[string]any{line(courts[2], hour, 1)},
		"guest": map[string]any{"name": "No Show CB", "phone": "+6281390001003"}, "via": "phone"}, "Idempotency-Key", newKey()).JSON()
	sa.Must(200, "POST", "/api/v1/sportclub/bookings/"+str(booking(b3)["id"])+":no-show", map[string]any{"reason": "did not come"})

	// tomorrow: discount and move by the supervisor, then void
	tomorrow := hour.AddDate(0, 0, 1)
	t10 := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 10, 0, 0, 0, f.Loc)
	b4 := sa.Must(201, "POST", "/api/v1/sportclub/bookings", map[string]any{"lines": []map[string]any{line(courts[0], t10, 1)},
		"guest": map[string]any{"name": "Move CB", "phone": "+6281390001004"}, "via": "phone"}, "Idempotency-Key", newKey()).JSON()
	id4 := str(booking(b4)["id"])
	sa.Must(200, "POST", "/api/v1/sportclub/bookings/"+id4+":discount", map[string]any{"amount": "10000", "reason": "regular team"})
	sa.Must(200, "POST", "/api/v1/sportclub/bookings/"+id4+":move", map[string]any{"lineId": firstLine(b4), "courtId": courts[1], "start": rfc(t10.Add(2 * time.Hour)),
		"reason": "asked by the team"})
	if v := sa.Must(200, "POST", "/api/v1/sportclub/bookings/"+id4+":void", map[string]any{"reason": "keyed twice"}).JSON(); booking(v)["void"] != true {
		t.Fatalf("void: %v", v)
	}

	// blocks, court staff report, incident
	d3 := hour.AddDate(0, 0, 3).Format("2006-01-02")
	blk := sa.Must(200, "POST", "/api/v1/sportclub/blocks", map[string]any{"courtIds": []string{courts[2]}, "from": d3, "to": d3, "startTime": "06:00",
		"endTime": "08:00", "reason": "maintenance", "notes": "net repair"}).JSON()
	blocks := blk["blocks"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("block: %v", blk)
	}
	sa.Must(200, "POST", "/api/v1/sportclub/blocks/"+str(blocks[0])+":remove", map[string]any{"reason": "repaired early"})
	rep := sa.Must(201, "POST", "/api/v1/sportclub/court-reports", map[string]any{"courtId": courts[2], "kind": "issue", "hours": 1, "note": "wet floor"}).JSON()
	sa.Must(200, "POST", "/api/v1/sportclub/court-reports/"+str(rep["report"].(map[string]any)["id"])+":resolve", nil)
	inc := sa.Must(201, "POST", "/api/v1/sportclub/incidents", map[string]any{"category": "complaint", "severity": "low", "courtId": courts[1],
		"description": "lights flicker"}).JSON()
	sa.Must(200, "POST", "/api/v1/sportclub/incidents/"+str(inc["id"])+":close", map[string]any{"reason": "lamp replaced"})

	// recurring community booking: preview, create, pause, resume, extend, stop
	rec := map[string]any{"courtId": courts[2], "weekdays": []int{1, 2, 3, 4, 5, 6, 7}, "startTime": "20:00", "hours": 1,
		"startDate": hour.AddDate(0, 0, 5).Format("2006-01-02"), "endDate": hour.AddDate(0, 0, 9).Format("2006-01-02"), "paymentMode": "pay_per_visit",
		"guest": map[string]any{"name": "Komunitas CB", "phone": "+6281390001005"}, "skipConflicts": true}
	sa.Must(200, "POST", "/api/v1/sportclub/recurring:preview", rec)
	rg := sa.Must(201, "POST", "/api/v1/sportclub/recurring", rec, "Idempotency-Key", newKey()).JSON()
	rp := "/api/v1/sportclub/recurring/" + str(rg["id"])
	d6 := hour.AddDate(0, 0, 6).Format("2006-01-02")
	sa.Must(200, "POST", rp+":pause", map[string]any{"from": d6, "until": d6, "reason": "school holiday"})
	sa.Must(200, "POST", rp+":resume", map[string]any{})
	sa.Must(200, "POST", rp+":extend", map[string]any{"endDate": hour.AddDate(0, 0, 12).Format("2006-01-02")})
	sa.Must(200, "POST", rp+":stop", map[string]any{"reason": "season over"})

	// website: quote, a payment abandoned (the slot is free again), Cek Booking
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": map[string]any{"autoPay": false}})
	t.Cleanup(func() {
		pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": map[string]any{"autoPay": true}})
	})
	d2 := hour.AddDate(0, 0, 2)
	web := time.Date(d2.Year(), d2.Month(), d2.Day(), 9, 0, 0, 0, f.Loc)
	if q := pub.Must(200, "POST", "/api/v1/public/sport-club/quote", map[string]any{"propertyId": prop, "lines": []map[string]any{line(courts[0], web, 1)},
		"method": "qris"}).JSON(); q["lines"] == nil {
		t.Fatalf("website quote: %v", q)
	}
	guest := map[string]any{"name": "Web CB", "phone": "+6281390001006", "email": "webcb@site.test"}
	wb := pub.Must(201, "POST", "/api/v1/public/court-bookings", map[string]any{"propertyId": prop, "guest": guest, "courtId": courts[0],
		"start": rfc(web), "end": rfc(web.Add(time.Hour)), "payMethod": "qris", "terms": true}).JSON()
	ab := pub.Must(200, "POST", "/api/v1/public/court-bookings/"+str(wb["token"])+":abandon?propertyId="+prop, nil).JSON()
	if ab["status"] == "confirmed" {
		t.Fatalf("abandoned payment: %v", ab)
	}
	if lk := pub.Must(200, "POST", "/api/v1/public/court-bookings:lookup", map[string]any{"propertyId": prop, "code": wb["reference"],
		"contact": "081390001006"}).JSON(); lk["token"] != wb["token"] {
		t.Fatalf("Cek Booking: %v", lk)
	}

	// Member App: quote and a guest ticket charged to the member
	if q := mc.Must(200, "POST", "/api/v1/member/sport-club/quote", map[string]any{"lines": []map[string]any{line(courts[1], web, 1)}, "method": "qris"}).JSON(); q["lines"] == nil {
		t.Fatalf("member quote: %v", q)
	}
	for _, seg := range []string{"guest_of_member", "walk_in"} {
		rule(t, sa, map[string]any{"code": "CB-GYM-" + seg, "name": "Gym CB " + seg, "serviceType": "facility_entry", "itemRef": "CB-GYM", "segment": seg,
			"unit": "entry", "price": "50000", "revenueComponent": "sport_entry"})
	}
	sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "CB-GYM", "name": "Gym CB", "facilityType": "gym", "capacity": 40,
		"usageMode": "entry", "priceItem": "CB-GYM", "openingHours": allDay})
	if gt := mc.Must(201, "POST", "/api/v1/member/sport-club/guest-tickets", map[string]any{"guestName": "Teman CB", "guestPhone": "+6281390001007",
		"memberCharge": true}, "Idempotency-Key", newKey()).JSON(); gt["token"] == "" {
		t.Fatalf("guest ticket: %v", gt)
	}
}
