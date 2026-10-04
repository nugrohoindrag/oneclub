package e2e

import (
	"sync"
	"testing"
	"time"
)

var allDay = map[string]any{"weekday": []string{"00:00", "23:59"}, "weekend": []string{"00:00", "23:59"}, "holiday": []string{"00:00", "23:59"}}

func isWeekend(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }

// EP-14 acceptance: entry rates per segment and day type; Guest of Member
// needs the member present; Child Entry for a 13-year-old is refused; a 5x
// voucher loses one entry per visit and cannot be used twice concurrently.
func TestP2SportClubEntryAccess(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	for _, e := range []struct{ seg, wd, we string }{{"walk_in", "185000", "255000"}, {"guest_of_member", "145000", "210000"}, {"child", "95000", "135000"},
		{"family", "425000", "615000"}, {"member", "0", "0"}} {
		rule(t, sa, map[string]any{"code": "POOL-" + e.seg + "-WD", "name": "Pool " + e.seg, "serviceType": "facility_entry", "itemRef": "POOL",
			"segment": e.seg, "lineDayTypeId": f.Weekday, "unit": "entry", "price": e.wd, "revenueComponent": "sport_entry"})
		rule(t, sa, map[string]any{"code": "POOL-" + e.seg + "-WE", "name": "Pool " + e.seg + " weekend", "serviceType": "facility_entry", "itemRef": "POOL",
			"segment": e.seg, "lineDayTypeId": f.Weekend, "unit": "entry", "price": e.we, "revenueComponent": "sport_entry"})
	}
	pool := idOf(sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "POOL-OLY", "name": "Olympic Pool", "facilityType": "swimming_pool",
		"capacity": 200, "usageMode": "entry", "priceItem": "POOL", "openingHours": allDay}))
	fac := sa.Must(200, "GET", "/api/v1/sportclub/facilities/"+pool, nil).JSON()
	if fac["resourceId"] == nil {
		t.Fatalf("entry facility with capacity gets a capacity resource: %v", fac)
	}
	now := time.Now().In(f.Loc)
	want := "185000"
	if isWeekend(now) {
		want = "255000"
	}
	walk := sa.Must(201, "POST", "/api/v1/sportclub/entries", map[string]any{"facilityId": pool, "entryType": "walk_in_guest",
		"guest": map[string]any{"name": "Walk In Guest", "phone": "0812000111"}, "payment": map[string]any{"methodType": "qris"}}, "Idempotency-Key", newKey()).JSON()
	if walk["entry"].(map[string]any)["amount"] != want {
		t.Fatalf("walk-in entry %v, want %s", walk["entry"].(map[string]any)["amount"], want)
	}
	ticket := walk["entry"].(map[string]any)
	acc := sa.Must(200, "POST", "/api/v1/sportclub/access:validate", map[string]any{"code": ticket["qrToken"], "facilityId": pool, "terminal": "SPORT-1"}).JSON()
	if acc["result"] != "granted" || acc["credentialType"] != "ticket" {
		t.Fatalf("ticket access: %v", acc)
	}
	if again := sa.Must(200, "POST", "/api/v1/sportclub/access:validate", map[string]any{"code": ticket["qrToken"], "facilityId": pool}).JSON(); again["result"] != "denied" {
		t.Fatalf("a used ticket must be denied: %v", again)
	}

	// Member with Sport Club membership; Guest of Member requires the member present.
	prog := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "SPORT-SC", "name": "Sport Club", "programKind": "sport_club"}))
	ind, indPkg := membershipType(t, sa, prog, "SC-IND", "Individual", map[string]any{"entitlements": map[string]any{"memberRate": true, "freeEntry": true, "memberCharge": true}})
	member := customer(t, sa, "SC-MEMBER", "Sari Member", map[string]any{"birthDate": dateAgo(35, 0, 0)})
	activeMembership(t, sa, member, ind, indPkg, nil)
	card := sa.Must(200, "GET", "/api/v1/membership/cards?filter[customerId]="+member, nil).Items()[0]
	guest := map[string]any{"facilityId": pool, "entryType": "guest_of_member", "hostCustomerId": member, "guest": map[string]any{"name": "Teman Sari"}}
	if r := sa.Do("POST", "/api/v1/sportclub/entries", guest); r.Status != 409 {
		t.Fatalf("guest of member without the member checked in: %s", r)
	}
	ma := sa.Must(200, "POST", "/api/v1/sportclub/access:validate", map[string]any{"code": card["qrToken"], "facilityId": pool}).JSON()
	if ma["result"] != "granted" || ma["credentialType"] != "member_card" {
		t.Fatalf("member card access: %v", ma)
	}
	gom := sa.Must(201, "POST", "/api/v1/sportclub/entries", guest).JSON()["entry"].(map[string]any)
	wantG := "145000"
	if isWeekend(now) {
		wantG = "210000"
	}
	if gom["amount"] != wantG {
		t.Fatalf("guest of member %v want %s", gom["amount"], wantG)
	}
	mem := sa.Must(201, "POST", "/api/v1/sportclub/entries", map[string]any{"facilityId": pool, "entryType": "member", "customerId": member}).JSON()["entry"].(map[string]any)
	if mem["amount"] != "0" {
		t.Fatalf("member free entry: %v", mem)
	}
	// Child Entry: 13-year-old refused, 10-year-old priced.
	if r := sa.Do("POST", "/api/v1/sportclub/entries", map[string]any{"facilityId": pool, "entryType": "child", "guest": map[string]any{"name": "Anak 13"},
		"birthDate": dateAgo(13, 0, 1)}); r.Status != 422 {
		t.Fatalf("13-year-old child entry must be refused: %s", r)
	}
	child := sa.Must(201, "POST", "/api/v1/sportclub/entries", map[string]any{"facilityId": pool, "entryType": "child", "guest": map[string]any{"name": "Anak 10"},
		"birthDate": dateAgo(10, 0, 0)}).JSON()["entry"].(map[string]any)
	if (isWeekend(now) && child["amount"] != "135000") || (!isWeekend(now) && child["amount"] != "95000") {
		t.Fatalf("child entry: %v", child["amount"])
	}
	fam := sa.Must(201, "POST", "/api/v1/sportclub/entries", map[string]any{"facilityId": pool, "entryType": "family_package", "adults": 2, "children": 3,
		"guest": map[string]any{"name": "Keluarga Budi"}}).JSON()["entry"].(map[string]any)
	if fam["amount"] != "425000" && fam["amount"] != "615000" {
		t.Fatalf("family package: %v", fam)
	}
	if r := sa.Do("POST", "/api/v1/sportclub/entries", map[string]any{"facilityId": pool, "entryType": "family_package", "adults": 3, "children": 3,
		"guest": map[string]any{"name": "Too big"}}); r.Status != 422 {
		t.Fatalf("family composition: %s", r)
	}
	// Voucher 5x: one entry per visit; two receptions at once for the last entry.
	vt := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "POOL5", "name": "Voucher Kolam 5x", "kind": "quota",
		"category": "sport_entry", "unit": "entry", "faceValue": "5", "price": "635000", "applicableServices": []string{"facility_entry"}}))
	v := sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": vt, "customerId": f.CustomerB,
		"payment": map[string]any{"methodType": "cash"}}).JSON()["vouchers"].([]any)[0].(map[string]any)
	for i := 0; i < 4; i++ {
		sa.Must(201, "POST", "/api/v1/sportclub/entries", map[string]any{"facilityId": pool, "entryType": "voucher", "voucherCode": v["code"], "customerId": f.CustomerB},
			"Idempotency-Key", newKey())
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := sa.Do("POST", "/api/v1/sportclub/entries", map[string]any{"facilityId": pool, "entryType": "voucher", "voucherCode": v["code"], "customerId": f.CustomerB},
				"Idempotency-Key", newKey())
			if r.Status == 201 {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("last voucher entry used %d times", ok)
	}
	// Cancel an unused ticket (refunds), lockers, access history, occupancy, entries list.
	e2 := sa.Must(201, "POST", "/api/v1/sportclub/entries", map[string]any{"facilityId": pool, "entryType": "walk_in_guest", "guest": map[string]any{"name": "Batal"},
		"payment": map[string]any{"methodType": "cash"}}).JSON()["entry"].(map[string]any)
	sa.Must(200, "POST", "/api/v1/sportclub/entries/"+str(e2["id"])+":cancel", map[string]any{"reason": "changed mind"})
	locker := idOf(sa.Must(201, "POST", "/api/v1/sportclub/lockers", map[string]any{"code": "L-101", "name": "Locker 101", "area": "female"}))
	la := sa.Must(201, "POST", "/api/v1/sportclub/locker-assignments", map[string]any{"lockerId": locker, "customerId": member, "assignmentType": "daily"}).JSON()
	if r := sa.Do("POST", "/api/v1/sportclub/locker-assignments", map[string]any{"lockerId": locker, "guestName": "Other"}); r.Status != 409 {
		t.Fatalf("occupied locker: %s", r)
	}
	sa.Must(200, "POST", "/api/v1/sportclub/locker-assignments/"+str(la["id"])+":return", nil)
	sa.Must(201, "POST", "/api/v1/sportclub/locker-assignments", map[string]any{"lockerId": locker, "guestName": "Rental", "assignmentType": "rental", "fee": "50000",
		"payment": map[string]any{"methodType": "cash"}})
	if n := len(sa.Must(200, "GET", "/api/v1/sportclub/locker-assignments?filter[lockerId]="+locker, nil).Items()); n != 2 {
		t.Fatalf("locker history %d", n)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/sportclub/access-events?filter[facilityId]="+pool, nil).Items()); n < 3 {
		t.Fatalf("access history %d", n)
	}
	occ := sa.Must(200, "GET", "/api/v1/sportclub/occupancy", nil).Items()
	if len(occ) == 0 || occ[0]["entriesToday"].(float64) < 5 {
		t.Fatalf("occupancy %v", occ)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/sportclub/entries?filter[entryType]=voucher", nil).Items()); n != 5 {
		t.Fatalf("voucher entries %d", n)
	}
	// Offline reception: queued scans go through the sync endpoint.
	sync := sa.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []map[string]any{{"id": newKey(), "action": "sportclub.access_validate",
		"payload": map[string]any{"code": card["qrToken"], "facilityId": pool, "direction": "out"}}}}).JSON()
	if res := sync["results"].([]any)[0].(map[string]any); res["status"] != "accepted" {
		t.Fatalf("offline access: %v", res)
	}

	// Court booking with time band price, payment, booking access and Session Package.
	futsal := idOf(sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "FUTSAL", "name": "Futsal", "facilityType": "futsal",
		"usageMode": "slot_booking", "priceItem": "FUTSAL-X", "openingHours": allDay}))
	court := idOf(sa.Must(201, "POST", "/api/v1/sportclub/courts", map[string]any{"code": "FUTSAL-A", "name": "Futsal A (sintetis)", "facilityId": futsal}))
	rule(t, sa, map[string]any{"code": "FUTSAL-X", "name": "Futsal any time", "serviceType": "sport_court", "itemRef": "FUTSAL-X", "unit": "slot", "unitMinutes": 60, "price": "200000"})
	start := time.Now().Truncate(time.Hour)
	bk := sa.Must(201, "POST", "/api/v1/sportclub/bookings", map[string]any{"courtId": court, "start": rfc(start), "end": rfc(start.Add(time.Hour)),
		"guest": map[string]any{"name": "Futsal Team", "phone": "0813999"}, "payment": map[string]any{"methodType": "qris"}}, "Idempotency-Key", newKey()).JSON()
	res := bk["reservation"].(map[string]any)
	if bk["total"] != "200000" || res["status"] != "confirmed" {
		t.Fatalf("court booking: %v", bk)
	}
	ba := sa.Must(200, "POST", "/api/v1/sportclub/access:validate", map[string]any{"code": res["code"], "facilityId": futsal}).JSON()
	if ba["result"] != "granted" || ba["credentialType"] != "booking" {
		t.Fatalf("booking access: %v", ba)
	}
	pk := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "FUTSAL4X", "name": "Paket Futsal 4x", "kind": "quota",
		"category": "court_package", "unit": "session", "faceValue": "4", "price": "658000", "applicableServices": []string{"sport_court"}}))
	pv := sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": pk, "customerId": f.CustomerB,
		"payment": map[string]any{"methodType": "cash"}}).JSON()["vouchers"].([]any)[0].(map[string]any)
	pb := sa.Must(201, "POST", "/api/v1/sportclub/bookings", map[string]any{"courtId": court, "start": rfc(start.Add(48 * time.Hour)),
		"end": rfc(start.Add(49 * time.Hour)), "customerId": f.CustomerB, "packageCode": pv["code"]}).JSON()
	if pb["total"] != "0" {
		t.Fatalf("package booking: %v", pb)
	}
	if rem := sa.Must(200, "GET", "/api/v1/commercial/vouchers/"+str(pv["id"]), nil).JSON()["remainingQuantity"]; rem != "3" {
		t.Fatalf("package remaining %v", rem)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/sportclub/bookings?date="+start.In(f.Loc).Format("2006-01-02"), nil).Items()); n < 1 {
		t.Fatal("sport club bookings list")
	}
}

// EP-15 acceptance: Swimming member registration 100,000 + 4x 465,000;
// guest 200,000 + 535,000; the 5th attendance on a 4x package is refused.
func TestP2Classes(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	prog := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "SPORT-CL", "name": "Sport Club (classes)", "programKind": "sport_club"}))
	ind, indPkg := membershipType(t, sa, prog, "SC-CL", "Individual", map[string]any{"entitlements": map[string]any{"memberRate": true}})
	member := customer(t, sa, "CL-MEMBER", "Member Swimmer", nil)
	guest := customer(t, sa, "CL-GUEST", "Guest Swimmer", nil)
	activeMembership(t, sa, member, ind, indPkg, nil)
	pool := idOf(sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "POOL-TRAIN", "name": "Training Pool", "facilityType": "swimming_pool", "usageMode": "class"}))
	var instUser string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = 'role.instructor_coach@matrix.test'`, nil, &instUser)
	coach := idOf(sa.Must(201, "POST", "/api/v1/sportclub/instructors", map[string]any{"code": "COACH-1", "name": "Coach Dewi", "userId": instUser,
		"disciplines": []string{"swimming"}, "feeScheme": "per_session", "feeRate": "150000"}))
	swim := idOf(sa.Must(201, "POST", "/api/v1/sportclub/class-programs", map[string]any{"code": "SWIM-KIDS", "name": "Swimming Kids", "discipline": "swimming",
		"capacity": 10, "durationMinutes": 60, "facilityId": pool}))
	sched := sa.Must(201, "POST", "/api/v1/sportclub/class-schedules", map[string]any{"programId": swim, "instructorId": coach, "facilityId": pool,
		"weekdays": []int{1, 2, 3, 4, 5, 6, 7}, "startTime": "04:00", "startDate": dateAgo(0, 0, 3), "endDate": time.Now().AddDate(0, 0, 7).Format("2006-01-02")}).JSON()
	// Instructor / facility conflict is rejected.
	other := idOf(sa.Must(201, "POST", "/api/v1/sportclub/class-programs", map[string]any{"code": "AEROBIC", "name": "Aerobic", "discipline": "aerobic", "capacity": 20}))
	if r := sa.Do("POST", "/api/v1/sportclub/class-schedules", map[string]any{"programId": other, "instructorId": coach, "weekdays": []int{1, 2, 3, 4, 5, 6, 7},
		"startTime": "04:30", "startDate": time.Now().Format("2006-01-02"), "endDate": time.Now().AddDate(0, 0, 2).Format("2006-01-02")}); r.Status != 409 {
		t.Fatalf("instructor conflict: %s", r)
	}
	rule(t, sa, map[string]any{"code": "SWIM-REG-M", "name": "Swimming registration member", "serviceType": "class_registration", "itemRef": "SWIM-KIDS",
		"segment": "member", "unit": "registration", "price": "100000", "revenueComponent": "registration_fee"})
	rule(t, sa, map[string]any{"code": "SWIM-REG-G", "name": "Swimming registration guest", "serviceType": "class_registration", "itemRef": "SWIM-KIDS",
		"segment": "guest", "unit": "registration", "price": "200000", "revenueComponent": "registration_fee"})
	pk := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "SWIM-4X", "name": "Swimming 4x", "kind": "quota",
		"category": "class_package", "unit": "session", "faceValue": "4", "price": "535000", "validityMonths": 2, "applicableItems": []string{"SWIM-KIDS"},
		"revenueComponent": "class"}))
	rule(t, sa, map[string]any{"code": "SWIM-4X-M", "name": "Swimming 4x member", "serviceType": "voucher_sale", "itemRef": "SWIM-4X", "segment": "member", "unit": "package", "packageQuantity": 4, "price": "465000"})
	rule(t, sa, map[string]any{"code": "SWIM-4X-G", "name": "Swimming 4x guest", "serviceType": "voucher_sale", "itemRef": "SWIM-4X", "segment": "guest", "unit": "package", "packageQuantity": 4, "price": "535000"})
	me := sa.Must(201, "POST", "/api/v1/sportclub/enrollments", map[string]any{"programId": swim, "customerId": member, "payment": map[string]any{"methodType": "cash"}}).JSON()
	ge := sa.Must(201, "POST", "/api/v1/sportclub/enrollments", map[string]any{"programId": swim, "customerId": guest, "payment": map[string]any{"methodType": "cash"}}).JSON()
	if me["registrationFee"] != "100000" || me["segment"] != "member" || ge["registrationFee"] != "200000" {
		t.Fatalf("registration fees: member %v guest %v", me, ge)
	}
	if r := sa.Do("POST", "/api/v1/sportclub/enrollments", map[string]any{"programId": swim, "customerId": guest}); r.Status != 409 {
		t.Fatalf("double registration: %s", r)
	}
	mp := sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": pk, "customerId": member, "segment": "member",
		"payment": map[string]any{"methodType": "cash"}}).JSON()
	gp := sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": pk, "customerId": guest, "segment": "guest",
		"payment": map[string]any{"methodType": "cash"}}).JSON()
	if mp["vouchers"].([]any)[0].(map[string]any)["pricePaid"] != "465000" || gp["vouchers"].([]any)[0].(map[string]any)["pricePaid"] != "535000" {
		t.Fatalf("package prices: %v / %v", mp["vouchers"], gp["vouchers"])
	}
	sessions := sa.Must(200, "GET", "/api/v1/sportclub/class-sessions?filter[programId]="+swim+"&from="+dateAgo(0, 0, 3)+"&days=11", nil).Items()
	if len(sessions) != 11 {
		t.Fatalf("sessions generated: %d (schedule %v)", len(sessions), sched["id"])
	}
	coachC := roleUser(t, inst, "instructor_coach")
	for i := 0; i < 4; i++ {
		b := sa.Must(201, "POST", "/api/v1/sportclub/session-bookings", map[string]any{"sessionId": sessions[i]["id"], "customerId": guest}).JSON()
		coachC.Must(200, "POST", "/api/v1/sportclub/attendance", map[string]any{"sessionBookingId": b["id"], "status": "present"})
	}
	// 5th attendance on a 4x package: refused until a new package is bought.
	if r := sa.Do("POST", "/api/v1/sportclub/session-bookings", map[string]any{"sessionId": sessions[4]["id"], "customerId": guest}); r.Status != 409 {
		t.Fatalf("5th session on a 4x package must be refused: %s", r)
	}
	// member: excused keeps the quota; club cancellation returns it
	b1 := sa.Must(201, "POST", "/api/v1/sportclub/session-bookings", map[string]any{"sessionId": sessions[5]["id"], "customerId": member}).JSON()
	coachC.Must(200, "POST", "/api/v1/sportclub/attendance", map[string]any{"sessionBookingId": b1["id"], "status": "absent"})
	sa.Must(200, "POST", "/api/v1/sportclub/attendance", map[string]any{"sessionBookingId": b1["id"], "status": "excused"})
	b2 := sa.Must(201, "POST", "/api/v1/sportclub/session-bookings", map[string]any{"sessionId": sessions[6]["id"], "customerId": member}).JSON()
	sa.Must(200, "POST", "/api/v1/sportclub/attendance", map[string]any{"sessionBookingId": b2["id"], "status": "present"})
	sa.Must(200, "POST", "/api/v1/sportclub/class-sessions/"+str(sessions[6]["id"])+":cancel", map[string]any{"reason": "pool maintenance"})
	if q := sa.Must(200, "GET", "/api/v1/commercial/prepaid-balances?filter[customerId]="+member, nil).Items(); len(q) != 0 {
		t.Fatalf("class packages are not prepaid balances: %v", q)
	}
	var rem string
	sysQueryRow(t, inst, `SELECT trim_scale(sum(remaining_quantity))::text FROM commercial.vouchers WHERE customer_id = $1`, []any{mustUUID(member)}, &rem)
	if rem != "4" {
		t.Fatalf("member quota after excused + cancelled session = %s, want 4", rem)
	}
	roster := sa.Must(200, "GET", "/api/v1/sportclub/class-sessions/"+str(sessions[0]["id"])+"/roster", nil).Items()
	if len(roster) != 1 || roster[0]["status"] != "present" {
		t.Fatalf("roster %v", roster)
	}
	if n := len(coachC.Must(200, "GET", "/api/v1/sportclub/my-classes", nil).Items()); n == 0 {
		t.Fatal("instructor My Classes")
	}
	if n := len(sa.Must(200, "GET", "/api/v1/sportclub/enrollments?filter[programId]="+swim, nil).Items()); n != 2 {
		t.Fatalf("enrollments %d", n)
	}
	sa.Must(200, "POST", "/api/v1/sportclub/class-schedules/"+str(sched["id"])+":generate", nil)
	// Instructor fee: sessions held in the period × rate, approval, payment.
	fee := sa.Must(201, "POST", "/api/v1/sportclub/instructor-fees:calculate", map[string]any{"instructorId": coach, "periodStart": dateAgo(0, 0, 3),
		"periodEnd": time.Now().Format("2006-01-02")}).JSON()
	if fee["sessions"].(float64) < 3 || fee["status"] != "approved" {
		t.Fatalf("instructor fee: %v", fee)
	}
	paid := sa.Must(200, "POST", "/api/v1/sportclub/instructor-fees/"+str(fee["id"])+":pay", map[string]any{"methodType": "bank_transfer", "reference": "HON-1"}).JSON()
	if paid["status"] != "paid" {
		t.Fatalf("paid: %v", paid)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/sportclub/instructor-fees?filter[instructorId]="+coach, nil).Items()); n != 1 {
		t.Fatalf("instructor fees %d", n)
	}
}
