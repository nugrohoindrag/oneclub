package e2e

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// EP-02 acceptance: rate cards of Sport Club 2025, Bungalow and Meeting
// Package are modelled without code and resolve to the right prices.
func TestP2PricingRateCards(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	// Futsal synthetic, Mon–Thu: 07–16 = 175,000; 16–21 = 245,000 (nett).
	rule(t, sa, map[string]any{"code": "FUTSAL-MT-AM", "name": "Futsal Mon–Thu 07–16", "serviceType": "sport_court", "itemRef": "FUTSAL",
		"lineDayTypeId": f.MonThu, "timeBandId": f.Morning, "unit": "slot", "unitMinutes": 60, "price": "175000", "pricingMode": "nett",
		"taxCodes": []string{"P2VAT"}, "revenueComponent": "court"})
	rule(t, sa, map[string]any{"code": "FUTSAL-MT-PM", "name": "Futsal Mon–Thu 16–21", "serviceType": "sport_court", "itemRef": "FUTSAL",
		"lineDayTypeId": f.MonThu, "timeBandId": f.Evening, "unit": "slot", "unitMinutes": 60, "price": "245000", "pricingMode": "nett",
		"taxCodes": []string{"P2VAT"}, "revenueComponent": "court"})
	mon := nextWeekday(f.Loc, time.Monday, 1)
	tue := nextWeekday(f.Loc, time.Tuesday, 1)
	if got := total(price(t, sa, map[string]any{"serviceType": "sport_court", "itemRef": "FUTSAL", "start": rfc(at(mon, 10, 0)), "end": rfc(at(mon, 11, 0))})); got != "175000" {
		t.Fatalf("futsal Mon 10:00 = %s, want 175000", got)
	}
	p := price(t, sa, map[string]any{"serviceType": "sport_court", "itemRef": "FUTSAL", "start": rfc(at(tue, 17, 0)), "end": rfc(at(tue, 18, 0))})
	if total(p) != "245000" || str(p["timeBand"]) != "16-21" {
		t.Fatalf("futsal Tue 17:00 = %v", p)
	}
	// 2 hours = 2 slots
	if got := total(price(t, sa, map[string]any{"serviceType": "sport_court", "itemRef": "FUTSAL", "start": rfc(at(mon, 8, 0)), "end": rfc(at(mon, 10, 0))})); got != "350000" {
		t.Fatalf("futsal 2 h = %s", got)
	}
	// Conflicting rule (same dimensions, overlapping period) is rejected on save.
	r := sa.Do("POST", "/api/v1/commercial/pricing-rules", map[string]any{"code": "FUTSAL-DUP", "name": "dup", "serviceType": "sport_court", "itemRef": "FUTSAL",
		"lineDayTypeId": f.MonThu, "timeBandId": f.Morning, "unit": "slot", "unitMinutes": 60, "price": "1", "effectiveFrom": past()})
	if r.Status != http.StatusConflict {
		t.Fatalf("conflicting rule: %s", r)
	}
	// Package 4x sold as voucher_sale rule: 658,000 (day) / 920,000 (evening) — priced through pricing.
	rule(t, sa, map[string]any{"code": "FUTSAL-4X-DAY", "name": "Futsal 4x 07–16", "serviceType": "voucher_sale", "itemRef": "FUTSAL4X-DAY",
		"unit": "package", "packageQuantity": 4, "price": "658000"})
	if got := total(price(t, sa, map[string]any{"serviceType": "voucher_sale", "itemRef": "FUTSAL4X-DAY", "start": rfc(time.Now())})); got != "658000" {
		t.Fatalf("4x package = %s", got)
	}

	// Entry Sport Club: Walk In Guest 185,000 / 255,000; Guest of Member 145,000 / 210,000;
	// Children Under 12 95,000 / 135,000; Family Package 425,000 / 615,000.
	for _, e := range []struct {
		seg, wd, we string
	}{{"walk_in", "185000", "255000"}, {"guest_of_member", "145000", "210000"}, {"child", "95000", "135000"}, {"family", "425000", "615000"}} {
		rule(t, sa, map[string]any{"code": "ENTRY-" + e.seg + "-WD", "name": "Entry " + e.seg + " weekday", "serviceType": "facility_entry",
			"segment": e.seg, "lineDayTypeId": f.Weekday, "unit": "entry", "price": e.wd, "revenueComponent": "sport_entry"})
		rule(t, sa, map[string]any{"code": "ENTRY-" + e.seg + "-WE", "name": "Entry " + e.seg + " weekend", "serviceType": "facility_entry",
			"segment": e.seg, "lineDayTypeId": f.Weekend, "unit": "entry", "price": e.we, "revenueComponent": "sport_entry"})
		wed, sun := nextWeekday(f.Loc, time.Wednesday, 1), nextWeekday(f.Loc, time.Sunday, 1)
		if got := total(price(t, sa, map[string]any{"serviceType": "facility_entry", "segment": e.seg, "start": rfc(at(wed, 9, 0))})); got != e.wd {
			t.Fatalf("entry %s weekday = %s want %s", e.seg, got, e.wd)
		}
		if got := total(price(t, sa, map[string]any{"serviceType": "facility_entry", "segment": e.seg, "start": rfc(at(sun, 9, 0))})); got != e.we {
			t.Fatalf("entry %s weekend = %s want %s", e.seg, got, e.we)
		}
	}
	// Public holiday on a weekday uses the weekend rate.
	ph := nextWeekday(f.Loc, time.Wednesday, 8)
	sa.Must(201, "POST", "/api/v1/platform/calendar-dates", map[string]any{"date": ph.Format("2006-01-02"), "kind": "public_holiday", "name": "Test Holiday"})
	if got := total(price(t, sa, map[string]any{"serviceType": "facility_entry", "segment": "walk_in", "start": rfc(at(ph, 9, 0))})); got != "255000" {
		t.Fatalf("entry on public holiday = %s, want weekend 255000", got)
	}

	// Bungalow Birdie: Room Only 2 nights = 2 × 605,000; Long Stay (min 7 nights) 550,000 per night.
	ro := idOf(sa.Must(201, "POST", "/api/v1/commercial/rate-plans", map[string]any{"code": "ROOM_ONLY", "name": "Room Only", "serviceType": "bungalow", "minNights": 1}))
	ls := idOf(sa.Must(201, "POST", "/api/v1/commercial/rate-plans", map[string]any{"code": "LONG_STAY", "name": "Long Stay", "serviceType": "bungalow", "minNights": 7}))
	rule(t, sa, map[string]any{"code": "BIRDIE-RO", "name": "Birdie Room Only", "serviceType": "bungalow", "itemRef": "BIRDIE", "ratePlanId": ro,
		"unit": "night", "price": "605000", "revenueComponent": "bungalow"})
	rule(t, sa, map[string]any{"code": "BIRDIE-LS", "name": "Birdie Long Stay", "serviceType": "bungalow", "itemRef": "BIRDIE", "ratePlanId": ls,
		"unit": "night", "price": "550000", "revenueComponent": "bungalow"})
	d0 := nextWeekday(f.Loc, time.Thursday, 3)
	if got := total(price(t, sa, map[string]any{"serviceType": "bungalow", "itemRef": "BIRDIE", "ratePlan": "ROOM_ONLY",
		"start": rfc(at(d0, 14, 0)), "end": rfc(at(d0.AddDate(0, 0, 2), 12, 0))})); got != "1210000" {
		t.Fatalf("Birdie 2 nights = %s, want 1210000", got)
	}
	if got := total(price(t, sa, map[string]any{"serviceType": "bungalow", "itemRef": "BIRDIE", "ratePlan": "LONG_STAY",
		"start": rfc(at(d0, 14, 0)), "end": rfc(at(d0.AddDate(0, 0, 7), 12, 0))})); got != "3850000" {
		t.Fatalf("Birdie long stay 7 nights = %s, want 3850000", got)
	}
	if r := sa.Do("POST", "/api/v1/commercial/pricing:resolve", map[string]any{"serviceType": "bungalow", "itemRef": "BIRDIE", "ratePlan": "LONG_STAY",
		"start": rfc(at(d0, 14, 0)), "end": rfc(at(d0.AddDate(0, 0, 2), 12, 0))}); r.Status != 422 {
		t.Fatalf("long stay below minimum nights must not price: %s", r)
	}

	// Full Day Meeting 40 pax = 40 × 410,000 ++ (15.5%); below 30 pax rejected.
	fd := idOf(sa.Must(201, "POST", "/api/v1/commercial/package-rates", map[string]any{"code": "FULL_DAY", "name": "Full Day Meeting", "serviceType": "meeting_package",
		"durationMinutes": 540, "coffeeBreaks": 2, "minPax": 30}))
	rule(t, sa, map[string]any{"code": "MEETING-FULLDAY", "name": "Full Day Meeting Package", "serviceType": "meeting_package", "packageRateId": fd,
		"unit": "pax", "price": "410000", "pricingMode": "plus_plus", "taxCodes": []string{"P2SVC", "P2PB1"}, "revenueComponent": "meeting"})
	mp := price(t, sa, map[string]any{"serviceType": "meeting_package", "package": "FULL_DAY", "quantity": 40, "start": rfc(at(d0, 8, 0)), "persist": true})
	tax := mp["tax"].(map[string]any)
	if str(mp["gross"]) != "16400000" || str(tax["total"]) != "18942000" || str(tax["netAmount"]) != "16400000" {
		t.Fatalf("meeting 40 pax: %v", mp)
	}
	if mp["snapshotId"] == nil {
		t.Fatal("persist=true must store a snapshot")
	}
	snap := sa.Must(200, "GET", "/api/v1/commercial/pricing-snapshots/"+str(mp["snapshotId"]), nil).JSON()
	if str(snap["totalAmount"]) != "18942000" || str(snap["ruleCode"]) != "MEETING-FULLDAY" {
		t.Fatalf("snapshot %v", snap)
	}
	if r := sa.Do("POST", "/api/v1/commercial/pricing:resolve", map[string]any{"serviceType": "meeting_package", "package": "FULL_DAY", "quantity": 20,
		"start": rfc(at(d0, 8, 0))}); r.Status != 422 {
		t.Fatalf("meeting below 30 pax must be rejected: %s", r)
	}
	// Effective version is immutable; a new version is created with the same code.
	rules := sa.Must(200, "GET", "/api/v1/commercial/pricing-rules?filter[code]=FUTSAL-MT-AM", nil).Items()
	if len(rules) != 1 {
		t.Fatalf("rules: %v", rules)
	}
	if r := sa.Do("PATCH", "/api/v1/commercial/pricing-rules/"+str(rules[0]["id"]), map[string]any{"price": "1"}); r.Status != 409 {
		t.Fatalf("editing an effective rule must fail: %s", r)
	}
	sa.Must(200, "PATCH", "/api/v1/commercial/pricing-rules/"+str(rules[0]["id"]), map[string]any{"status": "active"})
	v2 := sa.Must(201, "POST", "/api/v1/commercial/pricing-rules", map[string]any{"code": "FUTSAL-MT-AM", "name": "Futsal Mon–Thu 07–16 (2027)",
		"serviceType": "sport_court", "itemRef": "FUTSAL", "lineDayTypeId": f.MonThu, "timeBandId": f.Morning, "unit": "slot", "unitMinutes": 60,
		"price": "185000", "effectiveFrom": rfc(time.Now().AddDate(1, 0, 0))}).JSON()
	if v2["version"].(float64) != 2 {
		t.Fatalf("new version: %v", v2)
	}
	if got := total(price(t, sa, map[string]any{"serviceType": "sport_court", "itemRef": "FUTSAL", "start": rfc(at(mon, 10, 0)), "end": rfc(at(mon, 11, 0))})); got != "175000" {
		t.Fatalf("future version must not change today's price: %s", got)
	}
	// Public structured rate table (website).
	rates := anon(t, inst).Must(200, "GET", "/api/v1/public/rates/sportclub?propertyId="+inst.Main.String(), nil).Items()
	if len(rates) < 4 {
		t.Fatalf("public rates: %v", rates)
	}
}

// EP-01 acceptance: capacity mode enforced in the database; multi-line
// reservations are all-or-nothing; exclusive mode unchanged.
func TestP2ReservationEngine(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	types := sa.Must(200, "GET", "/api/v1/reservation/resource-types", nil).Items()
	if len(types) < 10 {
		t.Fatalf("resource type registry: %d", len(types))
	}
	// 100 parallel requests for the last seat of a swimming class session.
	pool := resourceOf(t, sa, "SWIM-LANE-CLASS", "class_session", intp(1), nil)
	d := nextWeekday(f.Loc, time.Saturday, 2)
	var ok, conflict int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := sa.Do("POST", "/api/v1/reservation/reservations", map[string]any{"lines": []map[string]any{{"resourceId": pool,
				"start": rfc(at(d, 8, 0)), "end": rfc(at(d, 9, 0))}}, "guestName": "Swimmer", "confirm": true})
			switch r.Status {
			case 201:
				atomic.AddInt32(&ok, 1)
			case 409:
				atomic.AddInt32(&conflict, 1)
			default:
				t.Errorf("unexpected %s", r)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || conflict != 99 {
		t.Fatalf("capacity race: %d ok, %d conflict", ok, conflict)
	}
	var booked, capacity int
	sysQueryRow(t, inst, `SELECT booked, capacity FROM reservation.capacity_slots WHERE resource_id = $1`, []any{mustUUID(pool)}, &booked, &capacity)
	if booked != 1 || capacity != 1 {
		t.Fatalf("slot booked=%d capacity=%d", booked, capacity)
	}

	// Meeting room + 2 projectors fails entirely when only one projector is free.
	room := resourceOf(t, sa, "TEST-ROOM", "meeting_room", nil, nil)
	proj := resourceOf(t, sa, "PROJECTOR", "equipment", intp(1), nil)
	md := nextWeekday(f.Loc, time.Wednesday, 3)
	r := sa.Do("POST", "/api/v1/reservation/reservations", map[string]any{"lines": []map[string]any{
		{"resourceId": room, "start": rfc(at(md, 9, 0)), "end": rfc(at(md, 17, 0))},
		{"resourceId": proj, "start": rfc(at(md, 9, 0)), "end": rfc(at(md, 17, 0)), "quantity": 2}}, "corporateName": "PT Maju", "confirm": true})
	if r.Status != 409 {
		t.Fatalf("2 projectors with capacity 1 must fail: %s", r)
	}
	var allocs, slotsBooked int
	sysQueryRow(t, inst, `SELECT count(*) FROM reservation.allocations WHERE resource_id = $1 AND status IN ('held','confirmed')`, []any{mustUUID(room)}, &allocs)
	sysQueryRow(t, inst, `SELECT coalesce(sum(booked),0) FROM reservation.capacity_slots WHERE resource_id = $1`, []any{mustUUID(proj)}, &slotsBooked)
	if allocs != 0 || slotsBooked != 0 {
		t.Fatalf("no allocation may remain: room %d, projector %d", allocs, slotsBooked)
	}
	// With one projector it succeeds; buffers of the meeting room are locked too.
	res := sa.Must(201, "POST", "/api/v1/reservation/reservations", map[string]any{"lines": []map[string]any{
		{"resourceId": room, "start": rfc(at(md, 9, 0)), "end": rfc(at(md, 17, 0))},
		{"resourceId": proj, "start": rfc(at(md, 9, 0)), "end": rfc(at(md, 17, 0)), "quantity": 1}}, "corporateName": "PT Maju", "confirm": true}).JSON()
	if res["status"] != "confirmed" || len(res["lines"].([]any)) != 2 {
		t.Fatalf("meeting booking: %v", res)
	}
	// meeting_room buffer after = 30 min: 17:15 start overlaps the buffer.
	if r := sa.Do("POST", "/api/v1/reservation/holds", map[string]any{"lines": []map[string]any{{"resourceId": room,
		"start": rfc(at(md, 17, 15)), "end": rfc(at(md, 18, 0))}}}); r.Status != 409 {
		t.Fatalf("buffer must be locked: %s", r)
	}

	// Exclusive court: hold → confirm, overlap rejected, availability, cancel frees it.
	court := resourceOf(t, sa, "FUTSAL-1", "sport_court", nil, map[string]any{"priceItem": "FUTSAL"})
	cd := nextWeekday(f.Loc, time.Tuesday, 2)
	hold := sa.Must(201, "POST", "/api/v1/reservation/holds", map[string]any{"lines": []map[string]any{{"resourceId": court,
		"start": rfc(at(cd, 17, 0)), "end": rfc(at(cd, 18, 0))}}, "customerId": f.CustomerB, "charge": true, "segment": "walk_in"}, "Idempotency-Key", newKey()).JSON()
	if hold["status"] != "draft" || hold["holdExpiresAt"] == nil || hold["folioId"] == nil {
		t.Fatalf("hold: %v", hold)
	}
	if line := hold["lines"].([]any)[0].(map[string]any); str(line["amount"]) != "245000" {
		t.Fatalf("hold line price: %v", line)
	}
	if r := sa.Do("POST", "/api/v1/reservation/reservations", map[string]any{"lines": []map[string]any{{"resourceId": court,
		"start": rfc(at(cd, 17, 30)), "end": rfc(at(cd, 18, 30))}}}); r.Status != 409 {
		t.Fatalf("overlap must fail: %s", r)
	}
	av := sa.Must(200, "GET", "/api/v1/reservation/availability?resourceType=sport_court&resourceId="+court+"&date="+cd.Format("2006-01-02")+"&withPrice=true&segment=walk_in", nil).JSON()
	slots := av["resources"].([]any)[0].(map[string]any)["slots"].([]any)
	var at17, at10 map[string]any
	for _, s := range slots {
		sm := s.(map[string]any)
		st, _ := time.Parse(time.RFC3339, str(sm["start"]))
		switch st.In(f.Loc).Hour() {
		case 17:
			at17 = sm
		case 10:
			at10 = sm
		}
	}
	if at17["status"] != "reserved" || at10["status"] != "available" || str(at10["price"]) != "175000" {
		t.Fatalf("availability 17:00 %v / 10:00 %v", at17, at10)
	}
	hid := str(hold["id"])
	conf := sa.Must(200, "POST", "/api/v1/reservation/reservations/"+hid+":confirm", map[string]any{}).JSON()
	if conf["status"] != "confirmed" {
		t.Fatalf("confirm: %v", conf)
	}
	// pay the folio, then cancel within the free period → full refund, no fee
	folio := str(conf["folioId"])
	sa.Must(201, "POST", "/api/v1/billing/folios/"+folio+"/payments", map[string]any{"methodType": "qris", "amount": "245000"}, "Idempotency-Key", newKey())
	can := sa.Must(200, "POST", "/api/v1/reservation/reservations/"+hid+":cancel", map[string]any{"reason": "customer request"}).JSON()
	if can["fee"] != "0" || can["refunded"] != "245000" {
		t.Fatalf("cancel: %v", can)
	}
	sa.Must(201, "POST", "/api/v1/reservation/reservations", map[string]any{"lines": []map[string]any{{"resourceId": court,
		"start": rfc(at(cd, 17, 0)), "end": rfc(at(cd, 18, 0))}}, "confirm": true})

	// Reschedule, check-in, complete, no-show; history records each change.
	r2 := sa.Must(201, "POST", "/api/v1/reservation/reservations", map[string]any{"lines": []map[string]any{{"resourceId": court,
		"start": rfc(at(cd, 8, 0)), "end": rfc(at(cd, 9, 0))}}, "confirm": true, "channel": "walk_in"}).JSON()
	lineID := str(r2["lines"].([]any)[0].(map[string]any)["id"])
	r2id := str(r2["id"])
	sa.Must(200, "POST", "/api/v1/reservation/reservations/"+r2id+":reschedule", map[string]any{"lines": []map[string]any{{"lineId": lineID,
		"start": rfc(at(cd, 9, 0)), "end": rfc(at(cd, 10, 0))}}, "reason": "late"})
	sa.Must(200, "POST", "/api/v1/reservation/reservations/"+r2id+":check-in", map[string]any{})
	sa.Must(200, "POST", "/api/v1/reservation/reservations/"+r2id+":complete", map[string]any{})
	det := sa.Must(200, "GET", "/api/v1/reservation/reservations/"+r2id, nil).JSON()
	if len(det["history"].([]any)) != 4 || det["status"] != "completed" {
		t.Fatalf("history: %v", det["history"])
	}
	r3 := sa.Must(201, "POST", "/api/v1/reservation/reservations", map[string]any{"lines": []map[string]any{{"resourceId": court,
		"start": rfc(at(cd, 11, 0)), "end": rfc(at(cd, 12, 0))}}, "confirm": true}).JSON()
	sa.Must(200, "POST", "/api/v1/reservation/reservations/"+str(r3["id"])+":no-show", map[string]any{"reason": "did not come"})
	sa.Must(201, "POST", "/api/v1/reservation/reservations", map[string]any{"lines": []map[string]any{{"resourceId": court,
		"start": rfc(at(cd, 11, 0)), "end": rfc(at(cd, 12, 0))}}, "confirm": true})

	// Recurring: every Tuesday 4 weeks; a block on week 3 produces one conflict.
	block := sa.Must(201, "POST", "/api/v1/reservation/blocks", map[string]any{"resourceId": court, "start": rfc(at(cd.AddDate(0, 0, 14), 19, 0)),
		"end": rfc(at(cd.AddDate(0, 0, 14), 21, 0)), "reason": "maintenance"}).JSON()
	if block["kind"] != "block" {
		t.Fatalf("block: %v", block)
	}
	rec := sa.Must(200, "POST", "/api/v1/reservation/reservations:recurring", map[string]any{"lines": []map[string]any{{"resourceId": court,
		"start": rfc(at(cd, 19, 0)), "end": rfc(at(cd, 20, 0))}}, "occurrences": 4, "confirm": true, "guestName": "Futsal Club"}).Items()
	created, conflicts := 0, 0
	for _, x := range rec {
		if x["status"] == "created" {
			created++
		} else {
			conflicts++
		}
	}
	if created != 3 || conflicts != 1 {
		t.Fatalf("recurring: %v", rec)
	}
	cal := sa.Must(200, "GET", "/api/v1/reservation/calendar?resourceType=sport_court&from="+cd.Format("2006-01-02")+"&days=1", nil).Items()
	if len(cal) == 0 || len(cal[0]["entries"].([]any)) < 3 {
		t.Fatalf("calendar: %v", cal)
	}
	// Hold expiry releases capacity and the reservation.
	h2 := sa.Must(201, "POST", "/api/v1/reservation/holds", map[string]any{"lines": []map[string]any{{"resourceId": proj,
		"start": rfc(at(md.AddDate(0, 0, 7), 9, 0)), "end": rfc(at(md.AddDate(0, 0, 7), 10, 0))}}}).JSON()
	sysExec(t, inst, `UPDATE reservation.reservations SET hold_expires_at = now() - interval '1 minute' WHERE id = $1`, mustUUID(str(h2["id"])))
	sysExec(t, inst, `UPDATE reservation.capacity_allocations SET expires_at = now() - interval '1 minute' WHERE reservation_id = $1`, mustUUID(str(h2["id"])))
	if _, err := reservationExpire(inst); err != nil {
		t.Fatal(err)
	}
	if st := sa.Must(200, "GET", "/api/v1/reservation/reservations/"+str(h2["id"]), nil).JSON()["status"]; st != "expired" {
		t.Fatalf("hold should expire, got %v", st)
	}
	var left int
	sysQueryRow(t, inst, `SELECT coalesce(sum(booked),0) FROM reservation.capacity_slots WHERE resource_id = $1 AND lower(period) >= $2`,
		[]any{mustUUID(proj), at(md.AddDate(0, 0, 7), 0, 0)}, &left)
	if left != 0 {
		t.Fatalf("expired hold must release capacity, booked=%d", left)
	}
	if list := sa.Must(200, "GET", "/api/v1/reservation/reservations?filter[status]=confirmed&filter[businessLine]=sportclub", nil).Items(); len(list) == 0 {
		t.Fatal("list reservations")
	}
}
