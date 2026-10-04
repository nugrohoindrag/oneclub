package e2e

import (
	"testing"
	"time"
)

// EP-16/17/18 acceptance: a checked-in unit cannot be booked again for the
// same night; check-out closes the folio after the POS charge to the
// bungalow is paid; VIP Suite block + overtime; meeting room capacity per
// layout (60 pax classroom: Sapphire 30 refused, Jade 120 accepted).
func TestP2StayAndVenue(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	ro := idOf(sa.Must(201, "POST", "/api/v1/commercial/rate-plans", map[string]any{"code": "STAY_RO", "name": "Room Only", "serviceType": "bungalow",
		"minNights": 1, "facilityAccess": []string{"swimming_pool", "gym"}}))
	rule(t, sa, map[string]any{"code": "EAGLE-RO", "name": "Eagle Room Only", "serviceType": "bungalow", "itemRef": "EAGLE", "ratePlanId": ro,
		"unit": "night", "price": "850000", "revenueComponent": "bungalow"})
	eagle := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "EAGLE", "name": "Eagle", "maxAdults": 2, "bedrooms": 1}))
	b1 := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalows", map[string]any{"code": "E-01", "name": "Eagle 01", "typeId": eagle, "view": "golf"}))
	sa.Must(201, "POST", "/api/v1/stay/bungalows", map[string]any{"code": "E-02", "name": "Eagle 02", "typeId": eagle, "view": "lake"})
	arr := time.Now().In(f.Loc).AddDate(0, 0, -1).Format("2006-01-02") // stay that covers now (staying guest access)
	dep := time.Now().In(f.Loc).AddDate(0, 0, 1).Format("2006-01-02")
	guest := customer(t, sa, "STAY-GUEST", "Pembicara Seminar", map[string]any{"phone": "+6281222333"})
	st := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": eagle, "arrivalDate": arr, "departureDate": dep,
		"ratePlan": "STAY_RO", "customerId": guest, "payment": map[string]any{"methodType": "bank_transfer", "reference": "DP-1"}}, "Idempotency-Key", newKey()).JSON()
	stay := st["stay"].(map[string]any)
	if st["total"] != "1700000" || st["depositRequired"] != "850000" || st["reservation"].(map[string]any)["status"] != "confirmed" || stay["unitAssigned"] != false {
		t.Fatalf("bungalow booking: total %v deposit %v status %v", st["total"], st["depositRequired"], st["reservation"].(map[string]any)["status"])
	}
	// second booking of the type gets the other unit; a third is sold out
	other := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": eagle, "arrivalDate": arr, "departureDate": dep,
		"ratePlan": "STAY_RO", "guest": map[string]any{"name": "Second Guest", "phone": "0811000999"}}).JSON()["stay"].(map[string]any)
	if other["unitId"] == stay["unitId"] {
		t.Fatal("type booking must pick a free unit")
	}
	if r := sa.Do("POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": eagle, "arrivalDate": arr, "departureDate": dep,
		"ratePlan": "STAY_RO", "guest": map[string]any{"name": "Third"}}); r.Status != 409 {
		t.Fatalf("sold out: %s", r)
	}
	av := sa.Must(200, "GET", "/api/v1/stay/availability?from="+arr+"&nights=2", nil).Items()
	if len(av) == 0 {
		t.Fatal("availability")
	}
	sid := str(stay["id"])
	if r := sa.Do("POST", "/api/v1/stay/stays/"+sid+":check-in", map[string]any{}); r.Status != 422 {
		t.Fatalf("identity required at check-in: %s", r)
	}
	ci := sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":check-in", map[string]any{"idType": "ktp", "idNumber": "3671012345670001"}).JSON()["stay"].(map[string]any)
	if ci["status"] != "checked_in" || ci["idNumberMasked"] == "3671012345670001" {
		t.Fatalf("check-in: %v", ci)
	}
	// the checked-in unit cannot be booked for the same night from any channel
	if r := sa.Do("POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "unitId": stay["unitId"], "arrivalDate": arr, "departureDate": dep,
		"ratePlan": "STAY_RO", "guest": map[string]any{"name": "Web guest"}, "channel": "website"}); r.Status != 409 {
		t.Fatalf("double booking of a checked-in unit: %s", r)
	}
	// staying guest gets facility access per rate plan
	pool := idOf(sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "POOL-STAY", "name": "Resort Pool", "facilityType": "swimming_pool",
		"usageMode": "entry", "openingHours": allDay}))
	en := sa.Must(201, "POST", "/api/v1/sportclub/entries", map[string]any{"facilityId": pool, "entryType": "staying_guest", "customerId": guest}).JSON()["entry"].(map[string]any)
	if en["amount"] != "0" {
		t.Fatalf("staying guest entry must be free: %v", en)
	}
	code := st["reservation"].(map[string]any)["code"]
	if a := sa.Must(200, "POST", "/api/v1/sportclub/access:validate", map[string]any{"code": code, "facilityId": pool}).JSON(); a["result"] != "granted" || a["credentialType"] != "stay" {
		t.Fatalf("stay code access: %v", a)
	}
	// restaurant bill charged to the bungalow (Charge to Stay), then check-out
	pos := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"folioType": "walk_in", "businessLine": "pos", "customerId": guest}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+pos+"/charges", map[string]any{"businessLine": "pos", "revenueComponent": "fnb", "description": "Dinner", "amount": "275000"})
	sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": pos, "methodType": "folio_transfer", "amount": "275000",
		"tender": map[string]any{"targetFolioId": stay["folioId"]}})
	if r := sa.Do("POST", "/api/v1/stay/stays/"+sid+":check-out", map[string]any{"at": rfc(time.Now())}); r.Status != 409 {
		t.Fatalf("check-out with an open balance must fail: %s", r)
	}
	sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": str(stay["folioId"]), "methodType": "card", "amount": "1125000", "reference": "EDC"})
	co := sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":check-out", map[string]any{"at": rfc(time.Now())}).JSON()
	if co["stay"].(map[string]any)["status"] != "checked_out" || co["folio"].(map[string]any)["folio"].(map[string]any)["status"] != "closed" {
		t.Fatalf("check-out: %v", co["stay"])
	}
	if b := sa.Must(200, "GET", "/api/v1/stay/bungalows/"+str(stay["unitId"]), nil).JSON(); b["readiness"] != "not_ready" {
		t.Fatalf("unit after check-out: %v", b["readiness"])
	}
	sa.Must(204, "POST", "/api/v1/stay/bungalows/"+str(stay["unitId"])+":readiness", map[string]any{"readiness": "ready"})
	// assign unit, cancel, no-show on future stays
	fa := time.Now().In(f.Loc).AddDate(0, 0, 20).Format("2006-01-02")
	fd := time.Now().In(f.Loc).AddDate(0, 0, 22).Format("2006-01-02")
	fs := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": eagle, "arrivalDate": fa, "departureDate": fd,
		"ratePlan": "STAY_RO", "customerId": guest}).JSON()["stay"].(map[string]any)
	sa.Must(200, "POST", "/api/v1/stay/stays/"+str(fs["id"])+":assign-unit", map[string]any{"unitId": b1})
	sa.Must(200, "POST", "/api/v1/stay/stays/"+str(fs["id"])+":cancel", map[string]any{"reason": "event postponed"})
	ns := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": eagle, "arrivalDate": fa, "departureDate": fd,
		"ratePlan": "STAY_RO", "customerId": guest}).JSON()["stay"].(map[string]any)
	sa.Must(200, "POST", "/api/v1/stay/stays/"+str(ns["id"])+":no-show", map[string]any{"reason": "did not arrive"})

	// VIP Suite: 8-hour block weekday 4,000,000 nett; overtime 500,000 per hour.
	suite := idOf(sa.Must(201, "POST", "/api/v1/stay/vip-suites", map[string]any{"code": "VIP1", "name": "VIP Suite", "blockHours": 8,
		"facilities": []string{"karaoke", "bar", "massage_room"}}))
	rule(t, sa, map[string]any{"code": "VIP-WD", "name": "VIP Suite weekday", "serviceType": "vip_suite", "itemRef": "VIP1", "lineDayTypeId": f.Weekday,
		"unit": "block", "unitMinutes": 480, "price": "4000000", "overtimePrice": "500000", "revenueComponent": "vip_suite"})
	rule(t, sa, map[string]any{"code": "VIP-WE", "name": "VIP Suite weekend", "serviceType": "vip_suite", "itemRef": "VIP1", "lineDayTypeId": f.Weekend,
		"unit": "block", "unitMinutes": 480, "price": "5000000", "overtimePrice": "500000", "revenueComponent": "vip_suite"})
	wed := nextWeekday(f.Loc, time.Wednesday, 2)
	vip := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "vip_suite", "unitId": suite, "start": rfc(at(wed, 10, 0)), "customerId": guest}).JSON()
	if vip["total"] != "4000000" {
		t.Fatalf("VIP block: %v", vip["total"])
	}
	ext := sa.Must(200, "POST", "/api/v1/stay/stays/"+str(vip["stay"].(map[string]any)["id"])+":extend", map[string]any{"hours": 1}).JSON()
	if ext["total"] != "4500000" {
		t.Fatalf("VIP overtime: %v", ext["total"])
	}
	blocker := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "vip_suite", "unitId": suite, "start": rfc(at(wed, 19, 30)), "customerId": guest}).JSON()
	if r := sa.Do("POST", "/api/v1/stay/stays/"+str(vip["stay"].(map[string]any)["id"])+":extend", map[string]any{"hours": 1}); r.Status != 409 {
		t.Fatalf("extension into another booking: %s", r)
	}
	_ = blocker
	req := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "vip_suite", "unitId": suite, "start": rfc(at(wed.AddDate(0, 0, 1), 10, 0)),
		"guest": map[string]any{"name": "Web Request", "email": "vip@p2.test"}, "channel": "website", "request": true}).JSON()
	if req["stay"].(map[string]any)["status"] != "requested" {
		t.Fatalf("website VIP request: %v", req["stay"])
	}
	sa.Must(200, "POST", "/api/v1/stay/stays/"+str(req["stay"].(map[string]any)["id"])+":confirm", nil)

	// Meeting rooms: capacity per layout; Full Day package; equipment as quantity resource.
	sapphire := idOf(sa.Must(201, "POST", "/api/v1/stay/meeting-rooms", map[string]any{"code": "SAPPHIRE", "name": "Sapphire", "sizeSqm": "60"}))
	jade := idOf(sa.Must(201, "POST", "/api/v1/stay/meeting-rooms", map[string]any{"code": "JADE", "name": "Jade", "sizeSqm": "200"}))
	sa.Must(201, "POST", "/api/v1/stay/room-layouts", map[string]any{"meetingRoomId": sapphire, "layout": "classroom", "capacity": 30})
	sa.Must(201, "POST", "/api/v1/stay/room-layouts", map[string]any{"meetingRoomId": jade, "layout": "classroom", "capacity": 120})
	pk := idOf(sa.Must(201, "POST", "/api/v1/commercial/package-rates", map[string]any{"code": "MTG_FULL", "name": "Full Day", "serviceType": "meeting_package",
		"durationMinutes": 540, "coffeeBreaks": 2, "minPax": 30}))
	rule(t, sa, map[string]any{"code": "MTG-FULL", "name": "Full Day Meeting", "serviceType": "meeting_package", "packageRateId": pk, "unit": "pax",
		"price": "410000", "pricingMode": "plus_plus", "taxCodes": []string{"P2SVC", "P2PB1"}, "revenueComponent": "meeting"})
	rule(t, sa, map[string]any{"code": "PROJ", "name": "Projector", "serviceType": "equipment", "itemRef": "PROJ", "unit": "item", "price": "500000", "revenueComponent": "equipment"})
	proj := idOf(sa.Must(201, "POST", "/api/v1/stay/equipment", map[string]any{"code": "PROJ", "name": "Projector", "quantity": 2}))
	thu := nextWeekday(f.Loc, time.Thursday, 3)
	meeting := map[string]any{"kind": "meeting_room", "unitId": sapphire, "start": rfc(at(thu, 8, 0)), "layout": "classroom", "pax": 60, "packageCode": "MTG_FULL",
		"corporateName": "PT Maju Jaya", "guest": map[string]any{"name": "Ibu Rina", "email": "rina@maju.test"}}
	if r := sa.Do("POST", "/api/v1/stay/stays", meeting); r.Status != 422 {
		t.Fatalf("60 pax classroom in Sapphire (30) must be refused: %s", r)
	}
	meeting["unitId"] = jade
	meeting["pax"] = 40
	meeting["equipment"] = []map[string]any{{"equipmentId": proj, "quantity": 2}}
	meeting["eventSchedule"] = []map[string]any{{"time": "08:00", "item": "Registration"}, {"time": "10:00", "item": "Coffee break"}}
	mt := sa.Must(201, "POST", "/api/v1/stay/stays", meeting).JSON()
	// 40 × 410,000 ++ 15.5% = 18,942,000 + 2 projectors 1,000,000
	if mt["total"] != "19942000" || mt["stay"].(map[string]any)["end"] == nil {
		t.Fatalf("meeting total: %v", mt["total"])
	}
	meeting["unitId"], meeting["pax"] = sapphire, 20
	meeting["equipment"] = []map[string]any{{"equipmentId": proj, "quantity": 1}}
	if r := sa.Do("POST", "/api/v1/stay/stays", meeting); r.Status != 409 {
		t.Fatalf("no projector left: %s", r)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/stay/stays?filter[kind]=meeting_room", nil).Items()); n != 1 {
		t.Fatalf("meeting list %d", n)
	}
	sa.Must(200, "GET", "/api/v1/stay/stays/"+str(mt["stay"].(map[string]any)["id"]), nil)
}
