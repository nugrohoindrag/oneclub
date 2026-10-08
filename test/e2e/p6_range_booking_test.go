package e2e

import (
	"testing"
)

// Demo feedback (9 Oct 2026): the driving range is booked from the Member
// App, the website (no account) and the front desk — a bay and a time held
// in the Reservation Engine, or only the visit. Full bays refuse a bay
// booking but not a visit; a cancelled booking frees its bay; check-in
// gives the booked bay when it is free.
func TestRangeBookingChannels(t *testing.T) {
	f := setupP2(t)
	fd := login(t, inst, "front.desk@demo.oneclub.id", demoPassword)
	m := login(t, inst, "member@demo.oneclub.id", demoPassword)
	pub := anon(t, inst)
	for _, c := range []string{"FD-I1", "FD-I2"} {
		f.SA.Must(201, "POST", "/api/v1/golf/range-bays", map[string]any{"code": c, "name": "Indoor " + c, "area": "indoor"})
	}
	day := clubDay(inst, 2, isWeekday)
	free := func(c *Client, path string) int {
		for _, s := range c.Must(200, "GET", path, nil).Items() {
			if s["time"] == "10:00" {
				return int(s["freeBays"].(float64))
			}
		}
		t.Fatalf("no 10:00 slot in %s", path)
		return 0
	}
	if n := free(m, "/api/v1/member/golf/range-availability?date="+day+"&area=indoor&minutes=60"); n < 2 {
		t.Fatalf("indoor bays free at 10:00: %d", n)
	}
	book := map[string]any{"date": day, "time": "10:00", "minutes": 60, "area": "indoor", "reserveBay": true}

	// Member App and website each hold a bay at 10:00
	mb := m.Must(201, "POST", "/api/v1/member/golf/range-bookings", merge(book, map[string]any{"players": 2})).JSON()
	if mb["holdsBay"] != true || mb["bayCode"] == nil || mb["channel"] != "member_app" {
		t.Fatalf("member range booking: %v", mb)
	}
	wb := pub.Must(201, "POST", "/api/v1/public/golf/range-bookings", merge(book, map[string]any{"propertyId": inst.Main,
		"guest": map[string]any{"name": "Rangga Website", "phone": "+628129990071"}})).JSON()
	if wb["holdsBay"] != true || wb["channel"] != "website" || wb["bayCode"] == mb["bayCode"] {
		t.Fatalf("website range booking: %v", wb)
	}
	if n := free(pub, "/api/v1/public/golf/range-availability?propertyId="+str(inst.Main)+"&date="+day+"&area=indoor&minutes=60"); n != 0 {
		t.Fatalf("both indoor bays are held at 10:00: %d free", n)
	}

	// full: a bay booking is refused, a visit (balls at the counter) is not
	desk := merge(book, map[string]any{"time": "10:30", "guestName": "Rina Walk-in", "guestPhone": "+628129990072"})
	if r := fd.Do("POST", "/api/v1/golf/range-bookings", desk); r.Status != 409 {
		t.Fatalf("no bay free at 10:30: %s", r.String())
	}
	visit := fd.Must(201, "POST", "/api/v1/golf/range-bookings", merge(desk, map[string]any{"reserveBay": false})).JSON()
	if visit["holdsBay"] != false || visit["channel"] != "walk_in" {
		t.Fatalf("range visit: %v", visit)
	}

	// the member cancels: the bay is free again for the desk
	m.Must(200, "POST", "/api/v1/member/golf/range-bookings/"+str(mb["id"])+":cancel", map[string]any{})
	held := fd.Must(201, "POST", "/api/v1/golf/range-bookings", desk).JSON()
	if held["holdsBay"] != true {
		t.Fatalf("bay free after the cancellation: %v", held)
	}
	fd.Must(200, "POST", "/api/v1/golf/range-bookings/"+str(visit["id"])+":cancel", map[string]any{"reason": "Changed plans"})

	// check-in: the booked bay when it is free
	ci := fd.Must(200, "POST", "/api/v1/golf/range-bookings/"+str(wb["id"])+":check-in", nil).JSON()
	if ci["status"] != "checked_in" || ci["sessionStatus"] != "active" || ci["bayNow"] != wb["bayCode"] || ci["holdsBay"] != false {
		t.Fatalf("range check-in: %v", ci)
	}
	if n := len(fd.Must(200, "GET", "/api/v1/golf/range-bookings?date="+day, nil).Items()); n < 4 {
		t.Fatalf("range bookings of the day: %d", n)
	}
	fd.Must(200, "POST", "/api/v1/golf/range-sessions/"+str(ci["sessionId"])+":end", nil)
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
