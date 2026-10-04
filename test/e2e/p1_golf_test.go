package e2e

// P1 Golf Core MVP: tee sheet generation (EP-03), booking (EP-04/05),
// check-in, caddy, golf cart, starter (EP-09/10/11), rain check and the
// daily operation reports (EP-16).

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// FR-TEE-01/02 AC: the Modern Golf templates produce the right slots for a
// weekday, a weekend and a public holiday.
func TestP1TeeSheetTemplates(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	check := func(day, session string, tee int, first, last string, n int) {
		t.Helper()
		s := slotsOf(teeTimes(t, gm, course, day), session, tee)
		if len(s) != n || s[0]["localTime"] != first || s[len(s)-1]["localTime"] != last {
			var times []string
			for _, x := range s {
				times = append(times, str(x["localTime"]))
			}
			t.Fatalf("%s %s tee %d: want %d slots %s–%s, got %d %v", day, session, tee, n, first, last, len(s), times)
		}
	}
	wd := clubDay(inst, 3, isWeekday)
	check(wd, "morning", 1, "05:30", "08:10", 21)
	check(wd, "morning", 10, "05:30", "08:10", 21)
	check(wd, "afternoon", 1, "11:20", "14:00", 21)
	check(wd, "night", 1, "16:30", "19:58", 27)
	we := clubDay(inst, 1, isSaturday)
	check(we, "morning", 1, "05:37", "08:11", 23)
	check(we, "afternoon", 1, "12:02", "13:54", 17)

	// A weekday declared a public holiday uses the Weekend & PH templates.
	ph := clubDay(inst, 15, isWeekday)
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	cd := pa.Must(201, "POST", "/api/v1/platform/calendar-days", map[string]any{"day": ph, "name": "Test Holiday", "kind": "public_holiday"}).JSON()
	pa.Must(200, "PATCH", "/api/v1/platform/calendar-days/"+str(cd["id"]), map[string]any{"name": "Cuti Bersama (test)"})
	slots := teeTimes(t, gm, course, ph)
	if slots[0]["dayTypeCode"] != "WEEKEND" || slots[0]["localTime"] != "05:37" {
		t.Fatalf("public holiday slots: %v", slots[0])
	}
	pa.Must(204, "DELETE", "/api/v1/platform/calendar-days/"+str(cd["id"]), nil)

	// Template versioning + regeneration (FR-TEE-11): a temporary template
	// is created, edited and archived.
	var route string
	for _, r := range gm.Must(200, "GET", "/api/v1/golf/playing-routes?limit=50", nil).Items() {
		if r["code"] == "FRONT9" && str(r["courseId"]) == course {
			route = str(r["id"])
		}
	}
	tpl := gm.Must(201, "POST", "/api/v1/golf/tee-sheet-templates", map[string]any{"courseId": course, "code": "TEST-TWILIGHT", "name": "Twilight test",
		"dayTypeCode": "WEEKDAY", "session": "night", "startTime": "20:30", "endTime": "21:00", "intervalMinutes": 10, "startTees": "1",
		"minPlayers": 2, "maxPlayers": 4, "playingRouteId": route, "lighting": true, "effectiveFrom": ph}).JSON()
	gm.Must(200, "PATCH", "/api/v1/golf/tee-sheet-templates/"+str(tpl["id"]), map[string]any{"name": "Twilight test (edited)"})
	gm.Must(204, "DELETE", "/api/v1/golf/tee-sheet-templates/"+str(tpl["id"]), nil)
}

// EP-04/05/09/10/11: booking → payment → caddy & golf cart → check-in →
// starter queue (call, hold, release, skip, tee-off) → rain check → round
// finish, plus the daily reports.
func TestP1GolfDayOperation(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	cashier := login(t, inst, "cashier@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	day := clubDay(inst, 4, isWeekday)
	am := slotsOf(teeTimes(t, gm, course, day), "morning", 1)
	caddies := presentCaddies(t, gm, day)
	if len(caddies) < 10 {
		t.Fatalf("caddies present: %d", len(caddies))
	}

	// Booking A: member + guest of member (Member Rate 640k + Guest 995k).
	a := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "member", "channel": "back_office", "teeTimeId": am[2]["id"],
		"players": []map[string]any{{"playerType": "member", "memberNo": "D0001"},
			{"playerType": "guest_of_member", "name": "Andi Guest", "phone": "+628129990001", "hostIndex": 0}}}).JSON()
	if a["status"] != "confirmed" {
		t.Fatalf("back office booking should be confirmed (pay at venue): %v", a["status"])
	}
	eqAmount(t, "booking A charges", a["folio"].(map[string]any)["charges"], 640000+995000)
	// Booking B: two non-members.
	b := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "non_member", "channel": "walk_in", "teeTimeId": am[3]["id"],
		"contactName": "Budi Walk-in", "contactPhone": "+628129990002",
		"players": []map[string]any{{"playerType": "non_member", "name": "Budi Walk-in", "phone": "+628129990002"},
			{"playerType": "non_member", "name": "Citra Walk-in", "phone": "+628129990003"}}}).JSON()
	eqAmount(t, "booking B charges", b["folio"].(map[string]any)["charges"], 2*995000)

	// FR-CHK-02: payment before check-in.
	r := gm.Do("POST", "/api/v1/golf/check-ins", map[string]any{"method": "booking_code", "value": a["code"]})
	if r.Status != 409 || !strings.Contains(string(r.Body), "payment_required") {
		t.Fatalf("check-in before payment: %s", r.String())
	}
	payFolio(t, cashier, a)
	payFolio(t, cashier, b)

	// Caddy & golf cart (auto from the queue / Ready carts; 2 per buggy).
	ca := gm.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": firstFlight(a), "auto": true}).Items()
	if len(ca) != 2 {
		t.Fatalf("caddy assignments A: %v", ca)
	}
	eqAmount(t, "caddy fee held", ca[0]["feeAmount"], 150000)
	gm.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": firstFlight(a), "auto": true})
	cb := gm.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": firstFlight(b), "auto": true}).Items()
	gm.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": firstFlight(b), "auto": true})
	// replace a caddy (FR-CAD-06)
	var spare string
	for _, c := range gm.Must(200, "GET", "/api/v1/golf/caddy-availability?date="+day, nil).Items() {
		if c["status"] == "available" {
			spare = str(c["caddyId"])
			break
		}
	}
	gm.Must(200, "POST", "/api/v1/golf/caddy-assignments/"+str(cb[1]["id"])+":replace", map[string]any{"caddyId": spare, "reason": "Caddy unwell"})

	// Check-in by booking code (A) and by booking QR (B) → flights Ready.
	ci := gm.Must(200, "POST", "/api/v1/golf/check-ins", map[string]any{"method": "booking_code", "value": a["code"]}).JSON()
	if len(ci["flightsReady"].([]any)) != 1 {
		t.Fatalf("flight A should be ready: %v", ci)
	}
	ci = gm.Must(200, "POST", "/api/v1/golf/check-ins", map[string]any{"method": "booking_qr", "value": "oneclub:booking:" + str(b["qrToken"])}).JSON()
	if len(ci["flightsReady"].([]any)) != 1 {
		t.Fatalf("flight B should be ready: %v", ci)
	}

	// Starter queue (FR-CHK-07..10).
	fa, fb := firstFlight(a), firstFlight(b)
	q := gm.Must(200, "GET", "/api/v1/golf/starter-queue?courseId="+course+"&date="+day, nil).JSON()
	if len(q["active"].([]any)) != 2 || str(q["active"].([]any)[0].(map[string]any)["flightId"]) != fa {
		t.Fatalf("queue: %v", q["active"])
	}
	sq := "/api/v1/golf/starter-queue/"
	gm.Must(200, "POST", sq+fa+":call", map[string]any{})
	gm.Must(422, "POST", sq+fa+":hold", map[string]any{})
	gm.Must(200, "POST", sq+fa+":hold", map[string]any{"reason": "Waiting for the fourth golf cart"})
	gm.Must(409, "POST", sq+fa+":tee-off", map[string]any{})
	q = gm.Must(200, "POST", sq+fa+":release", map[string]any{}).JSON()
	if str(q["active"].([]any)[0].(map[string]any)["flightId"]) != fa {
		t.Fatal("a released flight keeps its preserved queue position")
	}
	q = gm.Must(200, "POST", sq+fa+":skip", map[string]any{}).JSON()
	if str(q["active"].([]any)[0].(map[string]any)["flightId"]) != fb {
		t.Fatal("skip moves the flight behind the next one")
	}
	// weather stop suspends tee-off (FR-CHK-10)
	gm.Must(200, "PUT", "/api/v1/golf/course-status/"+course, map[string]any{"weather": "lightning_warning", "notes": "Lightning 5 km"})
	gm.Must(409, "POST", sq+fb+":tee-off", map[string]any{})
	gm.Must(200, "PUT", "/api/v1/golf/course-status/"+course, map[string]any{"weather": "normal", "courseState": "open"})
	gm.Must(200, "POST", sq+fb+":tee-off", map[string]any{})
	gm.Must(200, "POST", sq+fa+":tee-off", map[string]any{})

	// Tips during the round (FR-CAD-08).
	gm.Must(201, "POST", "/api/v1/golf/caddy-tips", map[string]any{"assignmentId": ca[0]["id"], "amount": "50000", "method": "cash"})

	// B plays its full round; rain stops A after 9 holes (50% credit). The
	// rain check closes the round of A (FR-BKG-11).
	gm.Must(200, "POST", sq+fb+":finish", map[string]any{"holesPlayed": 18})
	rc := gm.Must(201, "POST", "/api/v1/golf/rain-checks", map[string]any{"flightId": fa, "holesPlayed": 9}).Items()
	if len(rc) != 2 {
		t.Fatalf("rain checks A: %v", rc)
	}
	credits := map[string]bool{}
	for _, x := range rc {
		credits[dec(x["creditAmount"]).String()] = true
	}
	if !credits["320000"] || !credits["497500"] {
		t.Fatalf("rain check credits should be 50%% of 640k and 995k: %v", rc)
	}
	gm.Must(409, "POST", sq+fa+":finish", map[string]any{"holesPlayed": 9})
	rcb := gm.Must(201, "POST", "/api/v1/golf/rain-checks:batch", map[string]any{"flights": []map[string]any{{"flightId": fb, "holesPlayed": 18}}}).Items()
	for _, x := range rcb {
		eqAmount(t, "no credit after a full round", x["creditAmount"], 0)
	}

	// FR-RPT-03 AC: Today's Players equals the Daily Tee Sheet Report.
	players := gm.Must(200, "GET", "/api/v1/golf/players?date="+day, nil).Items()
	rep := gm.Must(200, "GET", "/api/v1/reporting/reports/golf.daily_tee_sheet?params[date]="+day, nil).JSON()
	rows := rep["rows"].([]any)
	if len(rows) != len(players) || len(rows) < 4 {
		t.Fatalf("daily tee sheet report has %d rows, Today's Players %d", len(rows), len(players))
	}
	dash := gm.Must(200, "GET", "/api/v1/reporting/dashboards/golf-today?date="+day+"&courseId="+course, nil).JSON()
	for _, w := range dash["widgets"].([]any) {
		wm := w.(map[string]any)
		if wm["key"] == "todays_players" && int(wm["value"].(float64)) != len(players) {
			t.Fatalf("dashboard Today's Players %v vs %d", wm["value"], len(players))
		}
	}
	for _, code := range []string{"golf.bookings", "golf.caddy_assignments", "golf.golf_cart_usage", "golf.rain_checks", "billing.golf_revenue",
		"billing.daily_payments", "golf.no_show_cancellation"} {
		res := gm.Do("GET", "/api/v1/reporting/reports/"+code+"?params[from]="+day+"&params[to]="+day, nil)
		if res.Status != 200 && res.Status != 403 {
			t.Fatalf("report %s: %s", code, res.String())
		}
	}
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	rev := fin.Must(200, "GET", "/api/v1/reporting/reports/billing.golf_revenue", nil).JSON()
	liability := false
	for _, row := range rev["rows"].([]any) {
		rm := row.(map[string]any)
		if rm["componentName"] == "Caddy Fee" && rm["liability"] == true {
			liability = true
		}
	}
	if !liability {
		t.Fatalf("caddy fee must be reported as liability: %v", rev["rows"])
	}
}

// Booking changes: players, flights, reschedule, cancel (fee), no-show,
// holds, lockers, bags, golf carts and course blocks.
func TestP1BookingChanges(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	day := clubDay(inst, 5, isWeekday)
	slots := teeTimes(t, gm, course, day)
	pm := slotsOf(slots, "afternoon", 1)
	caddies := presentCaddies(t, gm, day)

	bk := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "non_member", "channel": "back_office", "teeTimeId": pm[0]["id"],
		"paymentMode": "prepaid", "contactName": "Dina Change", "contactPhone": "+628129990010",
		"players": []map[string]any{{"playerType": "non_member", "name": "Dina Change", "phone": "+628129990010"},
			{"playerType": "non_member", "name": "Eko Change", "phone": "+628129990011"}}}).JSON()
	id := str(bk["id"])
	if bk["status"] != "pending" {
		t.Fatalf("prepaid booking waits for payment: %v", bk["status"])
	}
	bk = gm.Must(201, "POST", "/api/v1/golf/bookings/"+id+"/players", map[string]any{"player": map[string]any{"playerType": "non_member", "name": "Fani Third"}}).JSON()
	pids := playerIDs(bk)
	if len(pids) != 3 {
		t.Fatalf("players: %v", pids)
	}
	gm.Must(200, "PATCH", "/api/v1/golf/bookings/"+id+"/players/"+pids[2], map[string]any{"name": "Fani Ketiga", "phone": "+628129990012", "handicapIndex": "18.2"})
	gm.Must(200, "DELETE", "/api/v1/golf/bookings/"+id+"/players/"+pids[2]+"?reason=Cannot+come", nil)
	gm.Must(200, "POST", "/api/v1/golf/bookings/"+id+":confirm", nil)

	// Split a player to a new flight on the next tee time (FR-FLT-03); the
	// demo templates hold one flight per slot.
	gm.Must(409, "POST", "/api/v1/golf/flights", map[string]any{"teeTimeId": pm[0]["id"], "bookingId": id})
	fl := gm.Must(201, "POST", "/api/v1/golf/flights", map[string]any{"teeTimeId": pm[1]["id"], "bookingId": id}).JSON()
	gm.Must(200, "POST", "/api/v1/golf/flights/"+str(fl["id"])+"/players", map[string]any{"playerId": pids[1]})

	// Caddy queue reorder, assignment and cancel; golf cart readiness and return.
	gm.Must(200, "POST", "/api/v1/golf/caddy-queue:reorder", map[string]any{"date": day, "caddyIds": []string{caddies[1], caddies[0]}})
	ca := gm.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": firstFlight(bk), "assignments": []map[string]any{
		{"caddyId": caddies[1], "playerIds": []string{pids[0]}}}}).Items()
	gm.Must(200, "POST", "/api/v1/golf/caddy-assignments/"+str(ca[0]["id"])+":cancel", map[string]any{"reason": "Booking changed"})
	carts := gm.Must(200, "GET", "/api/v1/golf/golf-carts?limit=100", nil).Items()
	gm.Must(200, "POST", "/api/v1/golf/golf-carts/"+str(carts[29]["id"])+":set-readiness", map[string]any{"readiness": "maintenance", "reason": "Flat tyre"})
	// PRD P2 FR-CTL-02: back to Ready only through a passed (release) inspection.
	gm.Must(409, "POST", "/api/v1/golf/golf-carts/"+str(carts[29]["id"])+":set-readiness", map[string]any{"readiness": "ready"})
	gm.Must(201, "POST", "/api/v1/golf/golf-cart-inspections", map[string]any{"golfCartId": carts[29]["id"], "kind": "release",
		"results": []map[string]any{{"item": "Tyres", "pass": true}}})
	cart := gm.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": firstFlight(bk), "golfCartIds": []string{str(carts[28]["id"])}}).Items()
	gm.Must(200, "POST", "/api/v1/golf/golf-cart-assignments/"+str(cart[0]["id"])+":return", nil)

	// Lockers and bags (FR-CHK-04/05).
	lockers := gm.Must(200, "GET", "/api/v1/golf/lockers?limit=5", nil).Items()
	la := gm.Must(201, "POST", "/api/v1/golf/locker-assignments", map[string]any{"lockerId": lockers[0]["id"], "assignmentType": "daily", "bookingPlayerId": pids[0]}).JSON()
	gm.Must(409, "POST", "/api/v1/golf/locker-assignments", map[string]any{"lockerId": lockers[0]["id"], "assignmentType": "daily", "holderName": "Someone"})
	gm.Must(200, "POST", "/api/v1/golf/locker-assignments/"+str(la["id"])+":release", nil)
	bd := gm.Must(201, "POST", "/api/v1/golf/bag-drops", map[string]any{"bookingPlayerId": pids[0], "tagNumber": "TAG-" + day[8:], "bagCount": 1}).JSON()
	gm.Must(200, "POST", "/api/v1/golf/bag-drops/"+str(bd["id"])+":collect", nil)
	bs := gm.Must(201, "POST", "/api/v1/golf/bag-storage", map[string]any{"customerId": demoMemberCustomer(t, inst), "rackNumber": "R-07", "fee": "250000"}).JSON()
	gm.Must(200, "POST", "/api/v1/golf/bag-storage/"+str(bs["id"])+":end", nil)

	// A multi-flight booking is rescheduled per flight; a single-flight one
	// moves as a whole. Cancelling inside the free window has no fee.
	gm.Must(409, "POST", "/api/v1/golf/bookings/"+id+":reschedule", map[string]any{"teeTimeId": pm[3]["id"], "reason": "Customer request"})
	rs := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "non_member", "channel": "back_office", "teeTimeId": pm[9]["id"],
		"contactName": "Hari Move", "contactPhone": "+628129990030",
		"players": []map[string]any{{"playerType": "non_member", "name": "Hari Move", "phone": "+628129990030"},
			{"playerType": "non_member", "name": "Indra Move", "phone": "+628129990031"}}}).JSON()
	rs = gm.Must(200, "POST", "/api/v1/golf/bookings/"+str(rs["id"])+":reschedule", map[string]any{"teeTimeId": pm[11]["id"], "reason": "Customer request"}).JSON()
	if str(rs["teeTimeId"]) != str(pm[11]["id"]) || rs["rescheduleCount"].(float64) != 1 {
		t.Fatalf("reschedule: %v", rs)
	}
	for _, bid := range []string{id, str(rs["id"])} {
		c := gm.Must(200, "POST", "/api/v1/golf/bookings/"+bid+":cancel", map[string]any{"reason": "Weather forecast"}).JSON()
		if c["booking"].(map[string]any)["status"] != "cancelled" || !dec(c["fee"]).IsZero() {
			t.Fatalf("free cancellation: %v %v", c["booking"].(map[string]any)["status"], c["fee"])
		}
	}

	// No-show applies the 100% fee of the No-show Policy (FR-BKG-10).
	ns := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "non_member", "channel": "back_office", "teeTimeId": pm[5]["id"],
		"contactName": "Gita NoShow", "contactPhone": "+628129990020",
		"players": []map[string]any{{"playerType": "non_member", "name": "Gita NoShow", "phone": "+628129990020"},
			{"playerType": "non_member", "name": "Joko NoShow", "phone": "+628129990021"}}}).JSON()
	ns = gm.Must(200, "POST", "/api/v1/golf/bookings/"+str(ns["id"])+":no-show", map[string]any{"reason": "Did not arrive"}).JSON()
	if ns["status"] != "no_show" {
		t.Fatalf("no-show: %v", ns["status"])
	}
	rep := gm.Must(200, "GET", "/api/v1/reporting/reports/golf.no_show_cancellation?params[from]="+day+"&params[to]="+day, nil).JSON()
	if len(rep["rows"].([]any)) < 2 {
		t.Fatalf("no-show & cancellation report: %v", rep["rows"])
	}

	// Hold and release (FR-BKG-02).
	h := gm.Must(201, "POST", "/api/v1/golf/holds", map[string]any{"teeTimeId": pm[7]["id"], "players": 2}).JSON()
	gm.OK("DELETE", "/api/v1/golf/holds/"+str(h["id"]), nil)

	// Course block makes slots unbookable (FR-CRS-05).
	night := slotsOf(slots, "night", 1)
	start := night[0]["startAt"].(string)
	st, _ := time.Parse(time.RFC3339, start)
	blk := gm.Must(201, "POST", "/api/v1/golf/course-blocks", map[string]any{"courseId": course, "startsAt": st.Format(time.RFC3339),
		"endsAt": st.Add(30 * time.Minute).Format(time.RFC3339), "reason": "maintenance", "notes": "Bunker repair"}).JSON()
	gm.Must(409, "POST", "/api/v1/golf/holds", map[string]any{"teeTimeId": night[0]["id"], "players": 3})
	gm.Must(200, "POST", "/api/v1/golf/course-blocks/"+str(blk["id"])+":cancel", nil)

	// Handicap index (FR-CRS-07).
	gm.Must(201, "POST", "/api/v1/golf/handicaps", map[string]any{"customerId": demoMemberCustomer(t, inst), "handicapIndex": "13.8", "notes": "Club handicap review"})
}

// FR-BKG-02 AC: 200 parallel holds on the last seat produce exactly one
// hold; FR-BKG-03 AC: an expired hold frees the seat.
func TestP1ParallelHoldsAndExpiry(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	day := clubDay(inst, 6, isWeekday)
	am := slotsOf(teeTimes(t, gm, course, day), "morning", 10)
	slot := am[4]
	gm.Must(201, "POST", "/api/v1/golf/holds", map[string]any{"teeTimeId": slot["id"], "players": 3})

	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	clients := make([]*Client, 8)
	for i := range clients {
		clients[i] = login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	}
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			r := c.Do("POST", "/api/v1/golf/holds", map[string]any{"teeTimeId": slot["id"], "players": 1})
			mu.Lock()
			codes[r.Status]++
			mu.Unlock()
		}(clients[i%len(clients)])
	}
	wg.Wait()
	if codes[201] != 1 || codes[201]+codes[409] != 200 {
		t.Fatalf("parallel holds on the last seat: %v", codes)
	}

	// Expiry: age every hold of the slot and run the expiry job.
	sysExec(t, inst, `UPDATE golf.bookings SET hold_expires_at = now() - interval '1 minute' WHERE tee_time_id = $1 AND status = 'draft'`, slot["id"])
	if _, err := inst.App.Golf.ExpireHolds(t.Context()); err != nil {
		t.Fatal(err)
	}
	gm.Must(201, "POST", "/api/v1/golf/holds", map[string]any{"teeTimeId": slot["id"], "players": 4})
}

// NFR P1: a tee sheet change reaches subscribers through SSE within 2 s.
func TestP1TeeSheetRealtime(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	day := clubDay(inst, 7, isWeekday)
	pm := slotsOf(teeTimes(t, gm, course, day), "afternoon", 10)
	req, _ := http.NewRequestWithContext(t.Context(), "GET", inst.Server.URL+"/api/v1/golf/tee-sheet/stream?courseId="+course+"&date="+day, nil)
	req.Header.Set("X-Property-Id", inst.Main.String())
	req.Header.Set("Origin", inst.Origin)
	client := &http.Client{Jar: gm.http.Jar}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	events := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data:") {
				events <- sc.Text()
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	gm.Must(201, "POST", "/api/v1/golf/holds", map[string]any{"teeTimeId": pm[1]["id"], "players": 2})
	for {
		select {
		case ev := <-events:
			if strings.Contains(ev, "hold") || strings.Contains(ev, "booking") {
				if d := time.Since(start); d > 2*time.Second {
					t.Fatalf("SSE latency %s", d)
				}
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatal(fmt.Sprintf("no SSE event within 2 s of a hold on %s", day))
		}
	}
}
