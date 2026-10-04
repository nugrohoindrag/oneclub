package e2e

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// memberCustomer returns the customer linked to the matrix member user,
// creating it (with a member account) when TestP2MemberApp did not run.
func memberCustomer(t *testing.T, sa *Client) string {
	t.Helper()
	var cust string
	sysQueryRow(t, inst, `SELECT coalesce((SELECT id::text FROM crm.customers WHERE user_id = $1 LIMIT 1), '')`, []any{mustUUID(userID(t, "member"))}, &cust)
	if cust == "" {
		cust = customer(t, sa, "ZC-MEMBER", "Zaki Coverage", map[string]any{"email": "zaki@cov.test", "userId": userID(t, "member")})
		sa.Must(201, "POST", "/api/v1/billing/customer-accounts", map[string]any{"customerId": cust, "accountType": "member", "creditLimit": "10000000"})
	}
	return cust
}

// Golf operations not covered by the round / pace / reciprocal scenarios:
// cart readiness & maintenance & replacement, GPS, attendance, incidents,
// favourites & ratings (staff and member), tablet F&B order, official
// handicap, manual Hole-in-One, Hall of Fame curation, range bay assignment
// & cancel with a bridge-dispensed bucket, letter re-issue, and the
// member's own scorecard.
func TestP2GolfOperationsCoverage(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	mc := roleUser(t, inst, "member")
	me := memberCustomer(t, sa)
	g := setupGolfCourse(t, sa, "ZC")
	now := time.Now()
	day := clubDay(inst, 30, isWeekday)
	playDay, _ := time.ParseInLocation("2006-01-02", day, clubLoc(inst))

	// Caddies: attendance clock-in / clock-out, incident open / close.
	lvl := idOf(sa.Must(201, "POST", "/api/v1/golf/caddy-levels", map[string]any{"code": "ZC-L", "name": "Coverage", "rank": 9, "feeAmount": "100000"}))
	c1 := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "ZC01", "name": "Caddy Zeta"}))
	c2 := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "ZC02", "name": "Caddy Omega"}))
	c3 := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "ZC03", "name": "Caddy Sigma"}))
	sa.Must(200, "PUT", "/api/v1/golf/caddies/"+c1+"/profile", map[string]any{"levelId": lvl})
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": c2})
	if a := sa.Must(200, "POST", "/api/v1/golf/caddy-attendance:clock-out", map[string]any{"caddyId": c2}).JSON(); a["clockedOutAt"] == nil {
		t.Fatalf("clock-out: %v", a)
	}
	inc := sa.Must(201, "POST", "/api/v1/golf/caddy-incidents", map[string]any{"subjectType": "caddy", "caddyId": c2, "category": "late", "severity": "low",
		"description": "Arrived 20 minutes late"}).JSON()
	if cl := sa.Must(200, "POST", "/api/v1/golf/caddy-incidents/"+str(inc["id"])+":close", map[string]any{"reason": "Verbal warning"}).JSON(); cl["status"] != "closed" {
		t.Fatalf("incident close: %v", cl)
	}

	// Golf carts: readiness (P1), maintenance open / update, GPS fix.
	k1 := idOf(sa.Must(201, "POST", "/api/v1/golf/golf-carts", map[string]any{"code": "ZCB1", "name": "Buggy ZC1"}))
	k2 := idOf(sa.Must(201, "POST", "/api/v1/golf/golf-carts", map[string]any{"code": "ZCB2", "name": "Buggy ZC2"}))
	k3 := idOf(sa.Must(201, "POST", "/api/v1/golf/golf-carts", map[string]any{"code": "ZCB3", "name": "Buggy ZC3"}))
	sa.Must(200, "POST", "/api/v1/golf/golf-carts/"+k3+":set-readiness", map[string]any{"readiness": "out_of_service", "reason": "Waiting for parts"})
	mt := sa.Must(201, "POST", "/api/v1/golf/golf-cart-maintenance", map[string]any{"golfCartId": k3, "category": "battery", "description": "Battery replacement"}).JSON()
	if u := sa.Must(200, "PATCH", "/api/v1/golf/golf-cart-maintenance/"+str(mt["id"]), map[string]any{"cost": "4500000", "notes": "New lithium pack"}).JSON(); dec(u["cost"]).String() != "4500000" {
		t.Fatalf("maintenance update: %v", u)
	}
	// Both carts passed their inspection (the inspection flow is covered by TestP2GolfRound).
	sysExec(t, inst, `UPDATE golf.golf_carts SET readiness = 'ready' WHERE id = ANY($1)`, []uuid.UUID{mustUUID(k1), mustUUID(k2)})
	if r := sa.Must(200, "POST", "/api/v1/golf/golf-cart-positions", []map[string]any{{"golfCartId": k1, "lat": -6.2, "lng": 106.6, "batteryPercent": 88, "at": rfc(now)}}).JSON(); r["updated"].(float64) != 1 {
		t.Fatalf("gps ingest: %v", r)
	}

	// Round (P1 booking) with the member and a guest.
	bk, fid := golfBooking(t, sa, day, teeTimes(t, sa, g.Course, day)[0]["id"], []map[string]any{
		{"playerType": "non_member", "customerId": me, "name": "Zaki Coverage"}, {"playerType": "non_member", "customerId": f.CustomerB, "name": "Rina Tamu"}})
	players := bk["players"].([]any)
	myPlayer, guest := str(players[0].(map[string]any)["id"]), str(players[1].(map[string]any)["id"])
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": c1, "at": rfc(at(playDay, 5, 30))})
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": c3, "at": rfc(at(playDay, 5, 35))})
	ca := sa.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "assignments": []map[string]any{
		{"caddyId": c1, "playerIds": []string{myPlayer}}, {"caddyId": c3, "playerIds": []string{guest}}}}).Items()
	cart := sa.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "golfCartIds": []string{k1}}).Items()[0]
	checkIn(t, sa, day, bk)
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":start", map[string]any{"at": rfc(now.Add(-170 * time.Minute))})
	if r := sa.Must(200, "POST", "/api/v1/golf/golf-cart-assignments/"+str(cart["id"])+":replace", map[string]any{"golfCartId": k2, "reason": "Flat tyre"}).JSON(); r["flightId"] != fid {
		t.Fatalf("cart replacement: %v", r)
	}
	var myCard string
	for _, p := range sa.Must(200, "GET", "/api/v1/golf/rounds/"+fid, nil).JSON()["round"].(map[string]any)["players"].([]any) {
		if pm := p.(map[string]any); pm["id"] == myPlayer {
			myCard = str(pm["scorecardId"])
		}
	}
	if myCard == "" {
		t.Fatal("member scorecard")
	}
	// On-course F&B from the tablet, charged to the member's round folio.
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "ZC-HALF", "name": "Halfway ZC", "outletType": "halfway_house"}))
	prod := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "ZC-WATER", "name": "Mineral Water", "productType": "beverage", "price": "15000"}))
	if o := sa.Must(201, "POST", "/api/v1/golf/on-course-orders", map[string]any{"flightId": fid, "playerId": myPlayer, "outletId": outlet,
		"lines": []map[string]any{{"productId": prod, "quantity": "2"}}, "deliver": "halfway_house"}, "Idempotency-Key", newKey()).JSON(); o["source"] != "caddy_tablet" {
		t.Fatalf("tablet order: %v", o)
	}
	// The member keeps their own score in the app and submits the card.
	var entries []map[string]any
	for s := 1; s <= 18; s++ {
		entries = append(entries, map[string]any{"seq": s, "strokes": 5, "putts": 2})
	}
	if sc := mc.Must(200, "POST", "/api/v1/member/golf/scorecards/"+myCard+"/scores", map[string]any{"entries": entries}).JSON(); sc["gross"].(float64) != 90 {
		t.Fatalf("my scores: %v", sc)
	}
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":complete", map[string]any{})
	dispatch(t)
	if sc := mc.Must(200, "POST", "/api/v1/member/golf/scorecards/"+myCard+":submit", map[string]any{"attestedBy": "Rina Tamu"}).JSON(); sc["status"] != "submitted" {
		t.Fatalf("my card submitted: %v", sc)
	}
	// Ratings and favourites (member app and staff on behalf of the guest).
	mc.Must(204, "POST", "/api/v1/member/golf/caddy-assignments/"+str(ca[0]["id"])+":rate", map[string]any{"rating": 5, "comment": "Great reads"})
	sa.Must(204, "POST", "/api/v1/golf/caddy-ratings", map[string]any{"assignmentId": ca[1]["id"], "rating": 4, "customerId": f.CustomerB})
	mc.Must(204, "POST", "/api/v1/member/golf/caddies/"+c1+":favorite", map[string]any{"favorite": true})
	sa.Must(204, "POST", "/api/v1/golf/caddies/"+c1+":favorite", map[string]any{"customerId": f.CustomerB, "favorite": true})
	sa.Must(204, "POST", "/api/v1/golf/players/"+me+"/official-handicap", map[string]any{"index": "14.2", "source": "PGI"})

	// Manual Hole-in-One; Hall of Fame entry curated and consented by the member.
	if h := sa.Must(201, "POST", "/api/v1/golf/hole-in-ones", map[string]any{"customerId": me, "holeId": g.Holes[2]["holeId"], "achievedOn": now.Format("2006-01-02"),
		"caddyId": c1, "notes": "Recorded at the clubhouse"}).JSON(); h["status"] != "draft" {
		t.Fatalf("manual HIO: %v", h)
	}
	hof := idOf(sa.Must(201, "POST", "/api/v1/golf/hall-of-fame", map[string]any{"category": "club_champion", "title": "Club Champion 2026",
		"year": 2026, "customerId": me}))
	mc.Must(204, "POST", "/api/v1/member/golf/hall-of-fame/"+hof+":consent", map[string]any{"consent": "granted"})
	sa.Must(204, "POST", "/api/v1/golf/hall-of-fame/"+hof+":publish", nil)
	sa.Must(204, "POST", "/api/v1/golf/hall-of-fame/"+hof+":unpublish", nil)

	// Driving range: every bay busy → waiting guest gets a new bay; a session
	// is cancelled; a bucket dispensed through the bridge agent.
	var waiting map[string]any
	for i := 0; i < 30 && waiting == nil; i++ {
		s := sa.Must(201, "POST", "/api/v1/golf/range-sessions", map[string]any{"guestName": "ZC Range", "area": "outdoor"}).JSON()
		if s["status"] == "waiting" {
			waiting = s
		}
	}
	if waiting == nil {
		t.Fatal("range queue")
	}
	bay := idOf(sa.Must(201, "POST", "/api/v1/golf/range-bays", map[string]any{"code": "ZC-R9", "name": "Bay ZC", "area": "outdoor"}))
	if s := sa.Must(200, "POST", "/api/v1/golf/range-sessions/"+str(waiting["id"])+":assign-bay", map[string]any{"bayId": bay}).JSON(); s["status"] != "active" {
		t.Fatalf("assign bay: %v", s)
	}
	if s := sa.Must(200, "POST", "/api/v1/golf/range-sessions/"+str(waiting["id"])+":cancel", nil).JSON(); s["status"] != "cancelled" {
		t.Fatalf("cancel range session: %v", s)
	}
	ag := sa.Must(201, "POST", "/api/v1/platform/bridge-agents", map[string]any{"name": "Range Bridge (coverage)"}).JSON()
	agent := anon(t, inst)
	agent.Property = uuid.Nil
	agent.Must(200, "POST", "/api/v1/bridge/heartbeat", map[string]any{"agentVersion": "0.1.0", "hardware": []map[string]any{{"device": "zc_dispenser", "kind": "zc_dispenser"}}},
		"Authorization", "Bearer "+str(ag["token"]))
	if b := sa.Must(201, "POST", "/api/v1/golf/range-buckets", map[string]any{"balls": 40, "source": "complimentary", "reason": "Coverage clinic",
		"dispenser": "zc_dispenser"}).JSON(); b["dispenseMode"] != "bridge" {
		t.Fatalf("bridge bucket: %v", b)
	}
	hb := agent.Must(200, "POST", "/api/v1/bridge/heartbeat", map[string]any{"agentVersion": "0.1.0"}, "Authorization", "Bearer "+str(ag["token"])).JSON()
	cmds := hb["commands"].([]any)
	if len(cmds) != 1 {
		t.Fatalf("queued dispense command: %v", hb)
	}
	if r := agent.Must(200, "POST", "/api/v1/bridge/commands/"+str(cmds[0].(map[string]any)["id"])+":result", map[string]any{"success": true,
		"result": map[string]any{"dispensed": 40}}, "Authorization", "Bearer "+str(ag["token"])).JSON(); r["status"] != "succeeded" {
		t.Fatalf("command result: %v", r)
	}
	sa.Must(200, "PATCH", "/api/v1/platform/bridge-agents/"+str(ag["id"]), map[string]any{"status": "inactive"})

	// Introduction letters: member requests one in the app; a re-issue of an
	// approved letter renders the PDF again.
	club := idOf(sa.Must(201, "POST", "/api/v1/golf/reciprocal-clubs", map[string]any{"code": "ZC-CLUB", "name": "Coverage Golf Club", "country": "Malaysia",
		"agreementFrom": "2025-01-01", "agreementTo": "2030-12-31"}))
	prog := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "GOLF-ZC", "name": "Golf (coverage)", "programKind": "golf"}))
	typ, typPkg := membershipType(t, sa, prog, "GOLF-ZC-IND", "Golf Individual ZC", map[string]any{"annualFee": "1500000", "entitlements": map[string]any{"memberRate": true}})
	ms := activeMembership(t, sa, me, typ, typPkg, nil)
	today := now.Format("2006-01-02")
	l := mc.Must(201, "POST", "/api/v1/member/golf/introduction-letters", map[string]any{"clubId": club, "playFrom": today, "playTo": now.AddDate(0, 0, 2).Format("2006-01-02")}).JSON()
	if l["status"] != "issued" {
		t.Fatalf("my letter: %v", l)
	}
	sysExec(t, inst, `UPDATE golf.introduction_letters SET status = 'approved' WHERE id = $1`, mustUUID(str(l["id"])))
	if r := sa.Must(200, "POST", "/api/v1/golf/introduction-letters/"+str(l["id"])+":issue", nil).JSON(); r["status"] != "issued" || r["fileUrl"] == nil {
		t.Fatalf("re-issue: %v", r)
	}

	// Membership self-service: renew, pay the fee online, replace the card.
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": map[string]any{"autoPay": false}})
	if r := mc.Must(201, "POST", "/api/v1/member/memberships/"+ms+":renew", nil).JSON(); r["renewalId"] == nil {
		t.Fatalf("renew: %v", r)
	}
	// The next annual fee falls due today: the daily job schedules and charges it.
	sysExec(t, inst, `UPDATE membership.memberships SET next_fee_due = current_date WHERE id = $1`, mustUUID(ms))
	if _, err := inst.App.Membership.RunDaily(t.Context()); err != nil {
		t.Fatal(err)
	}
	var fee string
	for _, x := range mc.Must(200, "GET", "/api/v1/member/membership-fees", nil).Items() {
		if x["status"] == "due" {
			fee = str(x["id"])
		}
	}
	if co := mc.Must(201, "POST", "/api/v1/member/membership-fees/"+fee+":pay-online", map[string]any{"method": "qris"}).JSON(); co["status"] != "pending" {
		t.Fatalf("pay fee online: %v", co)
	}
	if c := mc.Must(201, "POST", "/api/v1/member/card:replace", map[string]any{"reason": "Lost wallet"}).JSON(); c["status"] != "active" {
		t.Fatalf("card replacement: %v", c)
	}
}

// Member App & website routes of Sport Club, Stay, CRM, Membership, Billing
// and POS that the main scenarios do not call.
func TestP2SelfServiceCoverage(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	mc := roleUser(t, inst, "member")
	me := memberCustomer(t, sa)
	pub := anon(t, inst)
	prop := inst.Main.String()
	day := time.Now().In(f.Loc).AddDate(0, 0, 4)

	// Court booking charged to my member account.
	fac := idOf(sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "ZC-BADM", "name": "Badminton ZC", "facilityType": "badminton",
		"usageMode": "slot_booking", "priceItem": "ZC-BADM", "openingHours": allDay}))
	court := idOf(sa.Must(201, "POST", "/api/v1/sportclub/courts", map[string]any{"code": "ZC-BADM-1", "name": "Badminton ZC 1", "facilityId": fac}))
	rule(t, sa, map[string]any{"code": "ZC-BADM", "name": "Badminton any time", "serviceType": "sport_court", "itemRef": "ZC-BADM", "unit": "slot", "unitMinutes": 60, "price": "80000"})
	start := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, f.Loc)
	if b := mc.Must(201, "POST", "/api/v1/member/sport-club/court-bookings", map[string]any{"courtId": court, "start": rfc(start), "end": rfc(start.Add(time.Hour)),
		"memberCharge": true}, "Idempotency-Key", newKey()).JSON(); b["reservation"] == nil && b["reservationId"] == nil && b["id"] == nil {
		t.Fatalf("my court booking: %v", b)
	}

	// Class: register in the app, buy the package, book a session; a
	// website visitor registers too.
	room := idOf(sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "ZC-STUDIO", "name": "Studio ZC", "facilityType": "studio", "usageMode": "class"}))
	coach := idOf(sa.Must(201, "POST", "/api/v1/sportclub/instructors", map[string]any{"code": "ZC-COACH", "name": "Coach Zed", "disciplines": []string{"aerobic"},
		"feeScheme": "per_session", "feeRate": "100000"}))
	yoga := idOf(sa.Must(201, "POST", "/api/v1/sportclub/class-programs", map[string]any{"code": "ZC-YOGA", "name": "Yoga ZC", "discipline": "aerobic",
		"capacity": 12, "durationMinutes": 60, "facilityId": room}))
	sa.Must(201, "POST", "/api/v1/sportclub/class-schedules", map[string]any{"programId": yoga, "instructorId": coach, "facilityId": room,
		"weekdays": []int{1, 2, 3, 4, 5, 6, 7}, "startTime": "18:00", "startDate": time.Now().Format("2006-01-02"), "endDate": time.Now().AddDate(0, 0, 6).Format("2006-01-02")})
	if e := mc.Must(201, "POST", "/api/v1/member/sport-club/class-enrollments", map[string]any{"programId": yoga, "memberCharge": true}, "Idempotency-Key", newKey()).JSON(); e["status"] != "active" {
		t.Fatalf("my class registration: %v", e)
	}
	pk := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "ZC-YOGA-4", "name": "Yoga 4x ZC", "kind": "quota",
		"category": "class_package", "unit": "session", "faceValue": "4", "price": "400000", "validityMonths": 2, "applicableItems": []string{"ZC-YOGA"}}))
	sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": pk, "customerId": me, "payment": map[string]any{"methodType": "cash"}},
		"Idempotency-Key", newKey())
	sessions := mc.Must(200, "GET", "/api/v1/member/sport-club/class-sessions?filter[programId]="+yoga+"&days=7", nil).Items()
	if len(sessions) == 0 {
		t.Fatal("bookable sessions")
	}
	if b := mc.Must(201, "POST", "/api/v1/member/sport-club/session-bookings", map[string]any{"sessionId": sessions[len(sessions)-1]["id"]}).JSON(); b["status"] != "booked" {
		t.Fatalf("my session booking: %v", b)
	}
	if pe := pub.Must(201, "POST", "/api/v1/public/class-enrollments", map[string]any{"propertyId": prop, "guest": map[string]any{"name": "Yogi Web",
		"email": "yogi@site.test"}, "programId": yoga, "birthDate": "1995-03-03"}).JSON(); pe["status"] != "active" {
		t.Fatalf("website class registration: %v", pe)
	}

	// Bungalow from the app on member charge.
	plan := idOf(sa.Must(201, "POST", "/api/v1/commercial/rate-plans", map[string]any{"code": "ZC_RO", "name": "Room Only (ZC)", "serviceType": "bungalow", "minNights": 1}))
	rule(t, sa, map[string]any{"code": "ZC-EAGLE-RO", "name": "Eagle ZC Room Only", "serviceType": "bungalow", "itemRef": "ZC-EAGLE", "ratePlanId": plan,
		"unit": "night", "price": "900000", "revenueComponent": "bungalow"})
	bt := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "ZC-EAGLE", "name": "Eagle ZC", "maxAdults": 2, "bedrooms": 1}))
	sa.Must(201, "POST", "/api/v1/stay/bungalows", map[string]any{"code": "ZC-E01", "name": "Eagle ZC 01", "typeId": bt})
	arr := day.AddDate(0, 0, 20)
	if s := mc.Must(201, "POST", "/api/v1/member/stays", map[string]any{"kind": "bungalow", "bungalowTypeId": bt, "arrivalDate": arr.Format("2006-01-02"),
		"departureDate": arr.AddDate(0, 0, 1).Format("2006-01-02"), "ratePlan": "ZC_RO", "adults": 2, "memberCharge": true}).JSON(); s["total"] != "900000" {
		t.Fatalf("my stay: %v", s)
	}

	// CRM: preferences removed by the member and by staff; feedback in the app.
	p1 := idOf(mc.Must(201, "POST", "/api/v1/member/preferences", map[string]any{"category": "beverage", "value": "Kopi tubruk ZC"}))
	mc.Must(204, "POST", "/api/v1/member/preferences/"+p1+":remove", nil)
	p2id := idOf(sa.Must(201, "POST", "/api/v1/crm/customers/"+me+"/preferences", map[string]any{"category": "food", "value": "No MSG ZC"}))
	sa.Must(204, "POST", "/api/v1/crm/preferences/"+p2id+":remove", nil)
	fr := idOf(sa.Must(201, "POST", "/api/v1/crm/feedback-requests", map[string]any{"customerId": me, "contextType": "sport", "contextLabel": "Badminton ZC"}))
	if fb := mc.Must(201, "POST", "/api/v1/member/feedback", map[string]any{"requestId": fr, "rating": 5, "comment": "Clean courts"}).JSON(); fb["lowScore"] == true {
		t.Fatalf("my feedback: %v", fb)
	}

	// Membership: application from the app; a draft application submitted
	// and force-activated by staff.
	sp := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "ZC-SPORT", "name": "Sport Club (coverage)", "programKind": "sport_club"}))
	st, _ := membershipType(t, sa, sp, "ZC-SC-IND", "Sport Individual ZC", map[string]any{"annualFee": "2000000"})
	if a := mc.Must(201, "POST", "/api/v1/member/membership-applications", map[string]any{"typeId": st, "notes": "Apply from the app"}).JSON(); a["applicationNo"] == "" {
		t.Fatalf("my application: %v", a)
	}
	applicant := customer(t, sa, "ZC-APPLICANT", "Calon ZC", map[string]any{"phone": "+6281299887766"})
	app := idOf(sa.Must(201, "POST", "/api/v1/membership/applications", map[string]any{"typeId": st, "customerId": applicant}))
	if a := sa.Must(200, "POST", "/api/v1/membership/applications/"+app+":submit", nil).JSON(); a["status"] == "draft" {
		t.Fatalf("application submit: %v", a)
	}
	if m := sa.Must(200, "POST", "/api/v1/membership/applications/"+app+":activate", map[string]any{"force": true}).JSON(); m["status"] != "active" {
		t.Fatalf("forced activation: %v", m)
	}

	// Billing: staff starts an online payment of a folio; POS order sent to the kitchen later.
	folio := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"customerId": me, "holderName": "Walk-in"}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+folio+"/lines", map[string]any{"chargeType": "other", "description": "Club rental", "unitPrice": "250000"}, "Idempotency-Key", newKey())
	if op := sa.Must(201, "POST", "/api/v1/billing/folios/"+folio+":online-payment", map[string]any{"method": "virtual_account"}).JSON(); op["amount"] != "250000" {
		t.Fatalf("staff online payment: %v", op)
	}
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "ZC-RESTO", "name": "Resto ZC", "outletType": "restaurant"}))
	prod := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "ZC-NASGOR", "name": "Nasi Goreng ZC", "productType": "food", "price": "65000"}))
	o := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "lines": []map[string]any{{"productId": prod, "quantity": "1"}}},
		"Idempotency-Key", newKey()).JSON()
	if s := sa.Must(200, "POST", "/api/v1/commercial/orders/"+str(o["id"])+":send", nil).JSON(); s["lines"].([]any)[0].(map[string]any)["sentAt"] == nil {
		t.Fatalf("order sent: %v", s)
	}
}
