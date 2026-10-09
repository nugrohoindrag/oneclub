package e2e

import (
	"testing"
	"time"
)

func init() {
	// validated by their hooks (a price / a discount) and unique per corporate account: covered by TestAccommodation
	for _, k := range []string{"stay.package", "stay.promotion", "stay.corporate_term"} {
		resourceCRUDSkip[k] = "covered by TestAccommodation"
	}
}

// Accommodation & Bungalow Management (docs/oneclub-accommodation-bungalow-
// management-requirements.md §37): room types and bungalows with rates per
// night (weekend, season with priority), rate plans with their policies,
// availability that excludes booked and Out of Order bungalows, the front
// office (room readiness, check-in, guest request, Charge to Room, check-out
// → Dirty → housekeeping → inspection → Ready), maintenance work orders,
// cancellation of a non-refundable rate, reschedule with a new price, the
// Stay X Pay Y promotion, the waitlist, the dashboard and role permissions.
func TestAccommodation(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	// the nightly room posting this test relies on (an earlier test may leave "at_booking")
	pcPolicy(t, sa, "Stay Policies", "stay.policy", map[string]any{"roomChargePosting": "nightly"})
	loc := clubLoc(inst)
	day := func(d time.Time) string { return d.Format("2006-01-02") }
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	mon := nextWeekday(loc, time.Monday, 3)
	fri := nextWeekday(loc, time.Friday, 3)

	// room types, bungalows, rates
	dlx := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "ACC-DLX", "name": "Deluxe Bungalow", "maxAdults": 2, "maxChildren": 1,
		"bedrooms": 1, "baseRate": "1000000", "weekendRate": "1300000", "bedConfiguration": "1 King", "sizeSqm": "42", "facilities": []string{"wifi", "ac"}}))
	fam := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "ACC-FAM", "name": "Family Bungalow", "maxAdults": 4, "maxChildren": 2,
		"bedrooms": 2, "baseRate": "2000000"}))
	unit := func(code, typ string) string {
		return idOf(sa.Must(201, "POST", "/api/v1/stay/bungalows", map[string]any{"code": code, "name": "Bungalow " + code, "typeId": typ, "location": "Lakeside"}))
	}
	d1, d2, d3 := unit("AC-D01", dlx), unit("AC-D02", dlx), unit("AC-D03", dlx)
	unit("AC-F01", fam)
	sa.Must(201, "POST", "/api/v1/stay/rate-plans", map[string]any{"code": "ACC-STD", "name": "Standard Rate", "planType": "standard", "derivation": "bar",
		"isDefault": true, "freeCancelHours": 24, "cancelFeePercent": "50"})
	sa.Must(201, "POST", "/api/v1/stay/rate-plans", map[string]any{"code": "ACC-MBR", "name": "Member Rate", "planType": "member", "derivation": "percent",
		"adjustValue": "-10", "eligibility": "member"})
	sa.Must(201, "POST", "/api/v1/stay/rate-plans", map[string]any{"code": "ACC-NR", "name": "Non-refundable", "planType": "promotional", "derivation": "amount",
		"adjustValue": "-100000", "nonRefundable": true, "paymentPolicy": "full_prepayment"})
	season := idOf(sa.Must(201, "POST", "/api/v1/stay/seasons", map[string]any{"code": "ACC-PEAK", "name": "Peak", "seasonType": "peak", "startDate": day(fri),
		"endDate": day(fri), "priority": 10}))
	sa.Must(201, "POST", "/api/v1/stay/season-prices", map[string]any{"seasonId": season, "bungalowTypeId": dlx, "weekdayPrice": "1500000", "weekendPrice": "1800000"})

	// search: weekday nights at the base rate, the plans derived from it
	rateOf := func(types []map[string]any, typ, code string) map[string]any {
		for _, ty := range types {
			if ty["typeId"] != typ {
				continue
			}
			for _, r := range asMaps(ty["rates"]) {
				if r["code"] == code {
					return r
				}
			}
		}
		return nil
	}
	search := func(a, d time.Time) []map[string]any {
		return sa.Must(200, "GET", "/api/v1/stay/search?arrival="+day(a)+"&departure="+day(d)+"&adults=2", nil).Items()
	}
	wk := search(mon, mon.AddDate(0, 0, 2))
	if r := rateOf(wk, dlx, "ACC-STD"); r == nil || !dec(r["total"]).Equal(dec("2000000")) {
		t.Fatalf("standard rate, 2 weekday nights: %v", r)
	}
	if r := rateOf(wk, dlx, "ACC-NR"); r == nil || !dec(r["total"]).Equal(dec("1800000")) || r["nonRefundable"] != true {
		t.Fatalf("non-refundable rate: %v", r)
	}
	if r := rateOf(wk, dlx, "ACC-MBR"); r == nil || r["error"] == nil {
		t.Fatalf("member rate refused for a guest: %v", r)
	}
	// Friday (peak season, weekend night) + Saturday (weekend rate)
	if r := rateOf(search(fri, fri.AddDate(0, 0, 2)), dlx, "ACC-STD"); r == nil || !dec(r["total"]).Equal(dec("3100000")) {
		t.Fatalf("season and weekend nights: %v", r)
	}

	// booking from yesterday: a booked bungalow cannot be booked twice
	guest := map[string]any{"name": "Anisa Rahma", "phone": "+628129990001", "email": "anisa@acc.test"}
	s1 := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "unitId": d1, "arrivalDate": day(today.AddDate(0, 0, -1)),
		"departureDate": day(today.AddDate(0, 0, 1)), "adults": 2, "guest": guest, "bookingSource": "phone", "specialRequests": "Late arrival"},
		"Idempotency-Key", newKey()).JSON()
	st1 := s1["stay"].(map[string]any)
	if st1["bookingSource"] != "phone" || st1["ratePlan"] != "ACC-STD" || !dec(st1["totalDue"]).IsPositive() {
		t.Fatalf("booking on the default rate plan: %v", st1)
	}
	sid := str(st1["id"])
	sa.Must(409, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "unitId": d1, "arrivalDate": day(today), "departureDate": day(today.AddDate(0, 0, 1)),
		"guest": map[string]any{"name": "Double"}})
	sa.Must(422, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": dlx, "arrivalDate": day(mon), "departureDate": day(mon.AddDate(0, 0, 1)),
		"adults": 3, "guest": map[string]any{"name": "Too many"}})

	// Out of Order: the bungalow is not offered
	blk := sa.Must(201, "POST", "/api/v1/stay/room-blocks", map[string]any{"bungalowId": d3, "kind": "out_of_order", "startDate": day(mon),
		"endDate": day(mon.AddDate(0, 0, 2)), "reason": "Roof repair"}).JSON()
	free := func(types []map[string]any, typ string) float64 {
		for _, ty := range types {
			if ty["typeId"] == typ {
				return ty["available"].(float64)
			}
		}
		return -1
	}
	if n := free(search(mon, mon.AddDate(0, 0, 2)), dlx); n != 2 {
		t.Fatalf("free deluxe with one Out of Order: %v", n)
	}
	sa.Must(409, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "unitId": d3, "arrivalDate": day(mon), "departureDate": day(mon.AddDate(0, 0, 1)),
		"guest": map[string]any{"name": "Blocked"}})
	sa.Must(200, "POST", "/api/v1/stay/room-blocks/"+str(blk["id"])+":release", map[string]any{"reason": "repaired early"})
	if n := free(search(mon, mon.AddDate(0, 0, 2)), dlx); n != 3 {
		t.Fatalf("free deluxe after the release: %v", n)
	}

	// front office: a Dirty bungalow cannot be checked in
	sa.Must(204, "POST", "/api/v1/stay/bungalows/"+d1+":room-status", map[string]any{"status": "dirty", "reason": "spot check"})
	sa.Must(409, "POST", "/api/v1/stay/stays/"+sid+":check-in", map[string]any{"idType": "ktp", "idNumber": "3171000011112222"})
	sa.Must(204, "POST", "/api/v1/stay/bungalows/"+d1+":room-status", map[string]any{"status": "ready"})
	ci := sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":check-in", map[string]any{"idType": "ktp", "idNumber": "3171000011112222"}).JSON()["stay"].(map[string]any)
	if ci["status"] != "checked_in" {
		t.Fatalf("check-in: %v", ci["status"])
	}
	if fo := sa.Must(200, "GET", "/api/v1/stay/front-office", nil).JSON(); len(asMaps(fo["inHouse"])) == 0 {
		t.Fatalf("in-house guests: %v", fo["counts"])
	}

	// paid guest request and Charge to Room on the folio
	towel := idOf(sa.Must(201, "POST", "/api/v1/stay/addons", map[string]any{"code": "ACC-TOWEL", "name": "Extra Towel", "category": "extra_towel", "price": "50000",
		"unit": "per_item", "pricingMode": "nett", "taxable": false, "serviceCharge": false, "availability": "in_stay"}))
	rq := sa.Must(201, "POST", "/api/v1/stay/guest-requests", map[string]any{"stayId": sid, "requestType": "extra_towel", "quantity": 2, "addonId": towel}).JSON()
	sa.Must(200, "POST", "/api/v1/stay/guest-requests/"+str(rq["id"])+":assign", map[string]any{"assignedTo": "Rudi HK"})
	if done := sa.Must(200, "POST", "/api/v1/stay/guest-requests/"+str(rq["id"])+":complete", map[string]any{}).JSON(); done["status"] != "completed" ||
		done["folioLineId"] == nil {
		t.Fatalf("paid request on the folio: %v", done)
	}
	sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":charge", map[string]any{"category": "minibar", "description": "Minibar", "unitPrice": "100000"})
	fol := sa.Must(200, "GET", "/api/v1/stay/stays/"+sid, nil).JSON()
	if !dec(fol["folio"].(map[string]any)["charges"]).Equal(dec("200000")) {
		t.Fatalf("folio charges (2 towels + minibar): %v", fol["folio"].(map[string]any)["charges"])
	}

	// in-house: details, late check-out, add-on added and removed, stayover cleaning
	sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":update", map[string]any{"vip": true, "notes": "Anniversary", "reason": "front office"})
	lc := sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":late-checkout", map[string]any{"until": "14:00", "reason": "flight at 18:00"}).JSON()
	if lc["stay"].(map[string]any)["lateCheckOutUntil"] == nil {
		t.Fatalf("late check-out: %v", lc["stay"])
	}
	bf := idOf(sa.Must(201, "POST", "/api/v1/stay/addons", map[string]any{"code": "ACC-BF", "name": "Breakfast", "category": "breakfast", "price": "150000",
		"unit": "per_person_night", "pricingMode": "nett", "taxable": false, "serviceCharge": false}))
	withAddon := sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+"/addons", map[string]any{"addonId": bf, "quantity": 1}).JSON()
	var added map[string]any
	for _, a := range asMaps(withAddon["addons"]) {
		if a["addonId"] == bf {
			added = a
		}
	}
	if added == nil || !dec(added["total"]).IsPositive() {
		t.Fatalf("add-on on the stay: %v", withAddon["addons"])
	}
	sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+"/addons/"+str(added["id"])+":void", map[string]any{"reason": "not wanted"})
	if plan := sa.Must(200, "POST", "/api/v1/stay/housekeeping:plan-day", map[string]any{}).Items(); len(plan) == 0 {
		t.Fatal("stayover cleaning for the in-house guest")
	}
	rq2 := sa.Must(201, "POST", "/api/v1/stay/guest-requests", map[string]any{"stayId": sid, "requestType": "laundry", "description": "2 shirts"}).JSON()
	sa.Must(200, "POST", "/api/v1/stay/guest-requests/"+str(rq2["id"])+":start", map[string]any{})
	sa.Must(200, "POST", "/api/v1/stay/guest-requests/"+str(rq2["id"])+":cancel", map[string]any{"notes": "guest changed mind"})
	if p := sa.Must(200, "GET", "/api/v1/stay/guests/"+str(st1["customerId"]), nil).JSON(); p["summary"] == nil {
		t.Fatalf("guest record: %v", p)
	}
	sa.Must(200, "PUT", "/api/v1/stay/guests/"+str(st1["customerId"])+"/profile", map[string]any{"nationality": "Indonesia",
		"preferences": "High floor, extra pillow", "vip": true})
	// the guest's own My Stay (reservation reference + e-mail) and a request from it
	my := sa.Must(200, "POST", "/api/v1/public/my-stay", map[string]any{"propertyId": st1["propertyId"], "reference": st1["stayNo"], "contact": "anisa@acc.test"}).JSON()
	if my["checkOutTime"] != "12:00" || len(asMaps(my["folio"])) == 0 {
		t.Fatalf("public My Stay: %v", my)
	}
	sa.Must(404, "POST", "/api/v1/public/my-stay", map[string]any{"propertyId": st1["propertyId"], "reference": st1["stayNo"], "contact": "someone@else.test"})
	sa.Must(201, "POST", "/api/v1/public/my-stay/requests", map[string]any{"propertyId": st1["propertyId"], "reference": st1["stayNo"],
		"contact": "anisa@acc.test", "requestType": "extra_towel", "quantity": 1})

	// check-out: the folio settled, the bungalow Dirty with a check-out cleaning
	fol = sa.Must(200, "GET", "/api/v1/stay/stays/"+sid, nil).JSON()
	due := str(fol["stay"].(map[string]any)["totalDue"])
	sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": str(st1["folioId"]), "methodType": "card", "amount": due, "reference": "EDC-ACC"})
	end := at(today.AddDate(0, 0, 1), 12, 0)
	co := sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":check-out", map[string]any{"at": rfc(end)}).JSON()
	if co["stay"].(map[string]any)["status"] != "checked_out" {
		t.Fatalf("check-out: %v", co["stay"])
	}
	roomOf := func(id string) map[string]any {
		for _, r := range sa.Must(200, "GET", "/api/v1/stay/room-status", nil).Items() {
			if r["id"] == id {
				return r
			}
		}
		return nil
	}
	if r := roomOf(d1); r["hkStatus"] != "dirty" {
		t.Fatalf("after check-out: %v", r["hkStatus"])
	}

	// housekeeping: Cleaning → Cleaned → inspection passed → Ready
	hk := roleUser(t, inst, "housekeeping")
	hk.Must(403, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "unitId": d2, "arrivalDate": day(mon), "departureDate": day(mon.AddDate(0, 0, 1)),
		"guest": map[string]any{"name": "Not allowed"}})
	var task map[string]any
	for _, x := range asMaps(hk.Must(200, "GET", "/api/v1/stay/housekeeping", nil).JSON()["tasks"]) {
		if x["bungalowId"] == d1 && x["taskType"] == "checkout_cleaning" {
			task = x
		}
	}
	if task == nil || task["source"] != "check_out" {
		t.Fatalf("check-out cleaning task: %v", task)
	}
	tid := str(task["id"])
	hk.Must(200, "POST", "/api/v1/stay/housekeeping-tasks/"+tid+":start", nil)
	if r := roomOf(d1); r["hkStatus"] != "cleaning" {
		t.Fatalf("cleaning: %v", r["hkStatus"])
	}
	hk.Must(200, "POST", "/api/v1/stay/housekeeping-tasks/"+tid+":complete", map[string]any{"notes": "done"})
	if r := roomOf(d1); r["hkStatus"] != "cleaned" {
		t.Fatalf("cleaned: %v", r["hkStatus"])
	}
	if ins := hk.Must(200, "POST", "/api/v1/stay/housekeeping-tasks/"+tid+":inspect", map[string]any{"result": "passed"}).JSON(); ins["result"] != "passed" {
		t.Fatalf("inspection: %v", ins)
	}
	if r := roomOf(d1); r["hkStatus"] != "ready" || r["operationalStatus"] != "ready" {
		t.Fatalf("ready after the inspection: %v", r)
	}

	hk.Must(403, "POST", "/api/v1/stay/housekeeping-tasks", map[string]any{"bungalowId": d3, "taskType": "deep_cleaning"})
	deep := sa.Must(201, "POST", "/api/v1/stay/housekeeping-tasks", map[string]any{"bungalowId": d3, "taskType": "deep_cleaning", "priority": "low"}).JSON()
	sa.Must(200, "POST", "/api/v1/stay/housekeeping-tasks/"+str(deep["id"])+":assign", map[string]any{"assignedTo": "Sari HK", "priority": "normal"})
	sa.Must(200, "POST", "/api/v1/stay/housekeeping-tasks/"+str(deep["id"])+":cancel", map[string]any{"reason": "next week"})
	hk.Must(200, "POST", "/api/v1/stay/bungalows/"+d3+":inspect", map[string]any{"result": "passed", "notes": "spot check"})

	// maintenance: an Out of Order work order until resolved
	wo := sa.Must(201, "POST", "/api/v1/stay/work-orders", map[string]any{"bungalowId": d2, "category": "ac", "title": "AC leaking", "priority": "high",
		"closesUnit": true}).JSON()
	if wo["blockId"] == nil || roomOf(d2)["operationalStatus"] != "out_of_order" {
		t.Fatalf("work order closes the bungalow: %v / %v", wo, roomOf(d2)["operationalStatus"])
	}
	sa.Must(409, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "unitId": d2, "arrivalDate": day(today), "departureDate": day(today.AddDate(0, 0, 1)),
		"guest": map[string]any{"name": "OOO"}})
	sa.Must(200, "POST", "/api/v1/stay/work-orders/"+str(wo["id"])+":assign", map[string]any{"assignedTo": "Tono Teknisi"})
	sa.Must(200, "POST", "/api/v1/stay/work-orders/"+str(wo["id"])+":start", nil)
	if res := sa.Must(200, "POST", "/api/v1/stay/work-orders/"+str(wo["id"])+":resolve", map[string]any{"resolution": "Drain cleaned", "cost": "150000"}).JSON(); res["status"] != "resolved" {
		t.Fatalf("resolved: %v", res)
	}
	if r := roomOf(d2); r["operationalStatus"] != "cleaned" {
		t.Fatalf("after the repair the bungalow waits for an inspection: %v", r["operationalStatus"])
	}
	sa.Must(200, "POST", "/api/v1/stay/work-orders/"+str(wo["id"])+":close", nil)
	minor := sa.Must(201, "POST", "/api/v1/stay/work-orders", map[string]any{"bungalowId": d3, "category": "furniture", "title": "Wobbly chair"}).JSON()
	sa.Must(200, "POST", "/api/v1/stay/work-orders/"+str(minor["id"])+":cancel", map[string]any{"reason": "duplicate"})
	ps := sa.Must(201, "POST", "/api/v1/stay/preventive-schedules", map[string]any{"code": "ACC-PM-AC", "name": "AC cleaning", "category": "ac",
		"bungalowId": d3, "intervalDays": 90, "nextDue": day(today)}).JSON()
	if wos := sa.Must(200, "POST", "/api/v1/stay/preventive-schedules:generate", map[string]any{}).Items(); len(wos) == 0 {
		t.Fatal("preventive work order due today")
	}
	if p := sa.Must(200, "GET", "/api/v1/stay/preventive-schedules/"+str(ps["id"]), nil).JSON(); p["nextDue"] == day(today) {
		t.Fatalf("next due moved: %v", p["nextDue"])
	}

	// cancellation: non-refundable keeps the whole stay, standard is free before the deadline
	nr := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": dlx, "arrivalDate": day(mon),
		"departureDate": day(mon.AddDate(0, 0, 2)), "ratePlan": "ACC-NR", "guest": map[string]any{"name": "Budi NR", "email": "budi.nr@acc.test"}}).JSON()
	if nr["depositRequired"] != "1800000" {
		t.Fatalf("full prepayment of the non-refundable rate: %v", nr["depositRequired"])
	}
	sa.Must(200, "POST", "/api/v1/stay/stays/"+str(nr["stay"].(map[string]any)["id"])+":cancel", map[string]any{"reason": "change of plans"})
	rv := sa.Must(200, "GET", "/api/v1/reservation/reservations/"+str(nr["reservation"].(map[string]any)["id"]), nil).JSON()
	if !dec(rv["cancellationFee"]).Equal(dec("1800000")) {
		t.Fatalf("non-refundable cancellation fee: %v", rv["cancellationFee"])
	}
	std := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": dlx, "arrivalDate": day(mon),
		"departureDate": day(mon.AddDate(0, 0, 2)), "guest": map[string]any{"name": "Citra Std", "email": "citra@acc.test"}}).JSON()
	if std["total"] != "2000000" {
		t.Fatalf("standard 2 nights: %v", std["total"])
	}
	// reschedule: one more night, price recalculated
	rs := sa.Must(200, "POST", "/api/v1/stay/stays/"+str(std["stay"].(map[string]any)["id"])+":reschedule", map[string]any{
		"departureDate": day(mon.AddDate(0, 0, 3)), "reason": "guest extends"}).JSON()
	if rs["stay"].(map[string]any)["nights"].(float64) != 3 || !dec(rs["total"]).Equal(dec("3000000")) {
		t.Fatalf("rescheduled: %v nights, total %v", rs["stay"].(map[string]any)["nights"], rs["total"])
	}
	if h := sa.Must(200, "GET", "/api/v1/stay/stays/"+str(std["stay"].(map[string]any)["id"])+"/history", nil).Items(); len(h) < 2 {
		t.Fatalf("audit trail: %v", h)
	}
	sa.Must(200, "POST", "/api/v1/stay/stays/"+str(std["stay"].(map[string]any)["id"])+":cancel", map[string]any{"reason": "free cancellation"})
	if rv := sa.Must(200, "GET", "/api/v1/reservation/reservations/"+str(std["reservation"].(map[string]any)["id"]), nil).JSON(); !dec(rv["cancellationFee"]).IsZero() {
		t.Fatalf("free cancellation: %v", rv["cancellationFee"])
	}

	// Stay 3 Pay 2 on the deluxe
	sa.Must(201, "POST", "/api/v1/stay/promotions", map[string]any{"code": "ACC-S3P2", "name": "Stay 3 Pay 2", "promoType": "stay_pay", "stayNights": 3,
		"payNights": 2, "bungalowTypeIds": []string{dlx}})
	pr := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": dlx, "arrivalDate": day(mon),
		"departureDate": day(mon.AddDate(0, 0, 3)), "guest": map[string]any{"name": "Dewi Promo"}}).JSON()
	if pr["total"] != "2000000" || pr["stay"].(map[string]any)["promotionCode"] != "ACC-S3P2" {
		t.Fatalf("stay 3 pay 2: %v / %v", pr["total"], pr["stay"].(map[string]any)["promotionCode"])
	}

	// waitlist: the family bungalow is full, a cancellation offers it
	fb := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": fam, "arrivalDate": day(mon),
		"departureDate": day(mon.AddDate(0, 0, 1)), "adults": 3, "guest": map[string]any{"name": "Eko Family"}}).JSON()
	sa.Must(409, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": fam, "arrivalDate": day(mon),
		"departureDate": day(mon.AddDate(0, 0, 1)), "guest": map[string]any{"name": "Fajar"}})
	wl := sa.Must(201, "POST", "/api/v1/stay/waitlist", map[string]any{"bungalowTypeId": fam, "arrivalDate": day(mon), "nights": 1, "adults": 2,
		"guest": map[string]any{"name": "Fajar Waiting", "email": "fajar@acc.test"}, "priority": "high"}).JSON()
	sa.Must(200, "POST", "/api/v1/stay/stays/"+str(fb["stay"].(map[string]any)["id"])+":cancel", map[string]any{"reason": "family cancelled"})
	var entry map[string]any
	for _, w := range sa.Must(200, "GET", "/api/v1/stay/waitlist", nil).Items() {
		if w["id"] == wl["id"] {
			entry = w
		}
	}
	if entry == nil || entry["status"] != "offered" {
		t.Fatalf("waitlist offered after the cancellation: %v", entry)
	}
	cv := sa.Must(201, "POST", "/api/v1/stay/waitlist/"+str(wl["id"])+":convert", map[string]any{}).JSON()
	if cv["stay"].(map[string]any)["status"] != "reserved" {
		t.Fatalf("waitlist converted: %v", cv["stay"])
	}

	sa.Must(409, "POST", "/api/v1/stay/waitlist/"+str(wl["id"])+":cancel", map[string]any{"reason": "converted already"})
	wl2 := sa.Must(201, "POST", "/api/v1/stay/waitlist", map[string]any{"bungalowTypeId": fam, "arrivalDate": day(mon.AddDate(0, 0, 7)), "nights": 1,
		"guest": map[string]any{"name": "Gita Later"}}).JSON()
	sa.Must(204, "POST", "/api/v1/stay/waitlist:offer", nil)
	sa.Must(200, "POST", "/api/v1/stay/waitlist/"+str(wl2["id"])+":cancel", map[string]any{"reason": "no longer needed"})

	// stay package: room + breakfast at one price per night
	pkg := sa.Must(201, "POST", "/api/v1/stay/packages", map[string]any{"code": "ACC-BB", "name": "Bed & Breakfast", "priceMode": "fixed", "price": "1400000",
		"inclusions": []map[string]any{{"addonId": bf, "quantity": 1}}, "bungalowTypeIds": []string{dlx}, "includesBreakfast": true}).JSON()
	sa.Must(422, "POST", "/api/v1/stay/packages", map[string]any{"code": "ACC-NOPRICE", "name": "No price", "priceMode": "fixed"})
	pb := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": dlx, "arrivalDate": day(mon.AddDate(0, 0, 14)),
		"departureDate": day(mon.AddDate(0, 0, 16)), "stayPackage": "ACC-BB", "adults": 2, "guest": map[string]any{"name": "Hana Package"}}).JSON()
	if pb["total"] != "2800000" || len(asMaps(pb["addons"])) != 1 || asMaps(pb["addons"])[0]["included"] != true {
		t.Fatalf("package stay: %v / %v", pb["total"], pb["addons"])
	}
	sa.Must(200, "PATCH", "/api/v1/stay/packages/"+str(pkg["id"]), map[string]any{"description": "Room and breakfast for two"})
	tmpPkg := sa.Must(201, "POST", "/api/v1/stay/packages", map[string]any{"code": "ACC-TMP", "name": "Temporary", "priceMode": "fixed", "price": "1"}).JSON()
	sa.Must(204, "DELETE", "/api/v1/stay/packages/"+str(tmpPkg["id"]), nil)
	promo := sa.Must(201, "POST", "/api/v1/stay/promotions", map[string]any{"code": "ACC-TMP", "name": "Temporary", "promoType": "percent",
		"discountPercent": "5", "promoCode": "TMP5"}).JSON()
	sa.Must(422, "POST", "/api/v1/stay/promotions", map[string]any{"code": "ACC-BAD", "name": "No discount", "promoType": "weekend"})
	sa.Must(200, "PATCH", "/api/v1/stay/promotions/"+str(promo["id"]), map[string]any{"description": "five percent"})
	sa.Must(204, "DELETE", "/api/v1/stay/promotions/"+str(promo["id"]), nil)

	// corporate booking: corporate rate plan, billing arrangement and a booking limit
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "ACC-CORP", "name": "PT Mitra Resort"}))
	terms := sa.Must(201, "POST", "/api/v1/stay/corporate-terms", map[string]any{"corporateAccountId": corp, "ratePlanCode": "ACC-STD",
		"billingArrangement": "company_pays_all", "maxRoomsPerNight": 1}).JSON()
	cs := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": dlx, "arrivalDate": day(mon.AddDate(0, 0, 21)),
		"departureDate": day(mon.AddDate(0, 0, 22)), "corporateAccountId": corp, "guest": map[string]any{"name": "Irfan Corporate"}}).JSON()["stay"].(map[string]any)
	if cs["bookingSource"] != "corporate" || cs["billingArrangement"] != "company_pays_all" || cs["corporateName"] != "PT Mitra Resort" {
		t.Fatalf("corporate stay: %v", cs)
	}
	sa.Must(409, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": dlx, "arrivalDate": day(mon.AddDate(0, 0, 21)),
		"departureDate": day(mon.AddDate(0, 0, 22)), "corporateAccountId": corp, "guest": map[string]any{"name": "Joko Corporate"}})
	sa.Must(200, "PATCH", "/api/v1/stay/corporate-terms/"+str(terms["id"]), map[string]any{"maxRoomsPerNight": 2})
	sa.Must(204, "DELETE", "/api/v1/stay/corporate-terms/"+str(terms["id"]), nil)

	// price summary without booking; no-show charge of the rate plan
	q := sa.Must(200, "POST", "/api/v1/stay/stays:quote", map[string]any{"kind": "bungalow", "bungalowTypeId": dlx, "arrivalDate": day(mon.AddDate(0, 0, 30)),
		"departureDate": day(mon.AddDate(0, 0, 32)), "guest": map[string]any{"name": "Quote"}}).JSON()
	if q["total"] != "2000000" {
		t.Fatalf("quote: %v", q["total"])
	}
	ns := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "unitId": d3, "arrivalDate": day(today.AddDate(0, 0, -1)),
		"departureDate": day(today.AddDate(0, 0, 1)), "guest": map[string]any{"name": "Kiki NoShow"}}).JSON()
	n := sa.Must(200, "POST", "/api/v1/stay/stays/"+str(ns["stay"].(map[string]any)["id"])+":no-show", map[string]any{"reason": "did not arrive"}).JSON()
	if !dec(n["stay"].(map[string]any)["noShowFee"]).IsPositive() {
		t.Fatalf("no-show charge: %v", n["stay"])
	}

	// the member's My Stay and a request from the Member App
	me := memberCustomer(t, sa)
	ms := sa.Must(201, "POST", "/api/v1/stay/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": dlx, "arrivalDate": day(mon.AddDate(0, 0, 40)),
		"departureDate": day(mon.AddDate(0, 0, 41)), "customerId": me}).JSON()["stay"].(map[string]any)
	mc := roleUser(t, inst, "member")
	if x := mc.Must(200, "GET", "/api/v1/member/stays/"+str(ms["id"])+"/experience", nil).JSON(); x["houseRules"] == "" {
		t.Fatalf("member My Stay: %v", x)
	}
	mc.Must(201, "POST", "/api/v1/member/stays/"+str(ms["id"])+"/requests", map[string]any{"requestType": "transportation", "description": "Airport pick-up"})

	// dashboard, report, room rack
	db := sa.Must(200, "GET", "/api/v1/stay/dashboard", nil).JSON()
	if db["roomStatus"] == nil || db["week"] == nil {
		t.Fatalf("dashboard: %v", db)
	}
	rep := sa.Must(200, "GET", "/api/v1/stay/accommodation-report?from="+day(today.AddDate(0, 0, -2))+"&to="+day(mon.AddDate(0, 0, 3)), nil).JSON()
	if rep["availableRoomNights"].(float64) <= 0 || rep["occupiedRoomNights"].(float64) <= 0 {
		t.Fatalf("report: %v", rep)
	}
	if rack := sa.Must(200, "GET", "/api/v1/stay/room-rack?from="+day(today)+"&days=10", nil).JSON(); len(asMaps(rack["units"])) < 4 {
		t.Fatalf("room rack: %v", rack["units"])
	}
}
