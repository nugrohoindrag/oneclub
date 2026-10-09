package e2e

import (
	"testing"
)

// Demo feedback (9 Oct 2026): the front desk switches the Indoor and
// Outdoor driving range areas on or off. A switched-off area is not
// offered in the Member App, on the website or at the desk, and the API
// refuses its availability and bookings.
func TestRangeAreasSwitch(t *testing.T) {
	f := setupP2(t)
	fd := login(t, inst, "front.desk@demo.oneclub.id", demoPassword)
	m := login(t, inst, "member@demo.oneclub.id", demoPassword)
	pub := anon(t, inst)
	// one bay per area, taken out again afterwards (other tests count the bays)
	for _, b := range []map[string]any{{"code": "AR-I1", "area": "indoor"}, {"code": "AR-O1", "area": "outdoor"}} {
		bay := f.SA.Must(201, "POST", "/api/v1/golf/range-bays", merge(b, map[string]any{"name": "Area test " + b["code"].(string)})).JSON()
		t.Cleanup(func() { f.SA.Must(200, "PATCH", "/api/v1/golf/range-bays/"+str(bay["id"]), map[string]any{"status": "inactive"}) })
	}
	day := clubDay(inst, 3, isWeekday)
	offered := func(c *Client, path string) map[string]bool {
		out := map[string]bool{}
		for _, a := range c.Must(200, "GET", path, nil).Items() {
			if path != "/api/v1/golf/range-areas" || a["offered"] == true {
				out[a["area"].(string)] = true
			}
		}
		return out
	}
	if a := offered(fd, "/api/v1/golf/range-areas"); !a["indoor"] {
		t.Fatalf("indoor with a bay is offered: %v", a)
	}

	fd.Must(200, "PUT", "/api/v1/golf/range-areas/indoor", map[string]any{"enabled": false})
	t.Cleanup(func() { fd.Must(200, "PUT", "/api/v1/golf/range-areas/indoor", map[string]any{"enabled": true}) })
	for _, a := range fd.Must(200, "GET", "/api/v1/golf/range-areas", nil).Items() {
		if a["area"] == "indoor" && (a["enabled"] != false || a["offered"] != false) {
			t.Fatalf("indoor switched off: %v", a)
		}
	}
	if a := offered(m, "/api/v1/member/golf/range-areas"); a["indoor"] || !a["outdoor"] {
		t.Fatalf("member app areas: %v", a)
	}
	if a := offered(pub, "/api/v1/public/golf/range-areas?propertyId="+str(inst.Main)); a["indoor"] || !a["outdoor"] {
		t.Fatalf("website areas: %v", a)
	}
	// refused by the API, not only hidden in the screens
	m.Must(422, "GET", "/api/v1/member/golf/range-availability?date="+day+"&area=indoor&minutes=60", nil)
	pub.Must(422, "POST", "/api/v1/public/golf/range-bookings", map[string]any{"propertyId": inst.Main, "date": day, "time": "11:00", "area": "indoor",
		"guest": map[string]any{"name": "Rangga Indoor", "phone": "+628129990081"}})
	fd.Must(422, "POST", "/api/v1/golf/range-bookings", map[string]any{"date": day, "time": "11:00", "area": "indoor", "guestName": "Rina Indoor"})
	// no area: the first area open for booking
	b := fd.Must(201, "POST", "/api/v1/golf/range-bookings", map[string]any{"date": day, "time": "11:00", "guestName": "Rina Outdoor"}).JSON()
	if b["area"] != "outdoor" {
		t.Fatalf("default area: %v", b)
	}
	fd.Must(200, "POST", "/api/v1/golf/range-bookings/"+str(b["id"])+":cancel", map[string]any{})
}
