package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

type golfCourse struct {
	Course, RouteAB, RouteA, TeeSet string
	Holes                           []map[string]any
}

// setupGolfCourse creates the course with sections A and B (9 holes each),
// a tee set, the playing routes A+B (18 holes) and A (9 holes) and a
// weekday morning tee sheet template on A+B (P1 course structure).
func setupGolfCourse(t *testing.T, c *Client, prefix string) golfCourse {
	t.Helper()
	venue := idOf(c.Must(201, "POST", "/api/v1/platform/venues", map[string]any{"code": prefix + "-V", "name": prefix + " Golf Venue", "venueType": "golf"}))
	g := golfCourse{}
	g.Course = idOf(c.Must(201, "POST", "/api/v1/golf/courses", map[string]any{"code": prefix, "name": prefix + " Course", "venueId": venue, "holes": 18}))
	pars := []int{4, 4, 3, 5, 4, 4, 3, 4, 5}
	// green and hazard points are P1 course assets (GeoJSON, [lng, lat])
	point := func(lat float64) map[string]any {
		return map[string]any{"type": "Point", "coordinates": []float64{106.6, lat}}
	}
	for si, sec := range []string{"A", "B"} {
		sid := idOf(c.Must(201, "POST", "/api/v1/golf/course-sections", map[string]any{"code": prefix + "-" + sec, "name": "Section " + sec,
			"courseId": g.Course, "sequence": si + 1}))
		for i, par := range pars {
			n := si*9 + i + 1
			code := fmt.Sprintf("%s-H%02d", prefix, n)
			hid := idOf(c.Must(201, "POST", "/api/v1/golf/holes", map[string]any{"courseId": g.Course, "sectionId": sid, "code": code, "number": n,
				"par": par, "strokeIndex": 2*i + 1 + si}))
			c.Must(204, "PUT", "/api/v1/golf/holes/"+hid+"/pace-target", map[string]any{"targetMinutes": 14})
			tee := -6.2 + float64(i)*0.001
			for _, a := range []struct {
				typ, name string
				lat       float64
			}{{"green_front", "Green front", tee + 0.003}, {"green_center", "Green center", tee + 0.0032}, {"green_back", "Green back", tee + 0.0034},
				{"hazard", "Bunker", tee + 0.002}} {
				c.Must(201, "POST", "/api/v1/golf/course-assets", map[string]any{"courseId": g.Course, "holeId": hid, "code": code + "-" + strings.ToUpper(a.typ),
					"assetType": a.typ, "name": a.name, "geometry": point(a.lat)})
			}
		}
	}
	g.TeeSet = idOf(c.Must(201, "POST", "/api/v1/golf/tee-sets", map[string]any{"code": prefix + "-BLUE", "name": "Blue", "courseId": g.Course,
		"courseRating": "72.0", "slope": 130}))
	g.RouteAB = idOf(c.Must(201, "POST", "/api/v1/golf/playing-routes", map[string]any{"code": prefix + "-AB", "name": "A+B", "courseId": g.Course,
		"sectionCodes": prefix + "-A," + prefix + "-B"}))
	c.Must(204, "PUT", "/api/v1/golf/playing-routes/"+g.RouteAB+"/pace-tolerance", map[string]any{"toleranceMinutes": 10})
	g.RouteA = idOf(c.Must(201, "POST", "/api/v1/golf/playing-routes", map[string]any{"code": prefix + "-A", "name": "A only", "courseId": g.Course,
		"sectionCodes": prefix + "-A"}))
	g.Holes = asMaps(c.Must(200, "GET", "/api/v1/golf/playing-routes/"+g.RouteAB+"/holes?teeSetId="+g.TeeSet, nil).JSON()["holes"])
	c.Must(201, "POST", "/api/v1/golf/tee-sheet-templates", map[string]any{"code": prefix + "-WD-AM", "name": prefix + " weekday morning", "courseId": g.Course,
		"dayTypeCode": "WEEKDAY", "session": "morning", "startTime": "06:00", "endTime": "08:00", "intervalMinutes": 10, "startTees": "1",
		"minPlayers": 1, "maxPlayers": 4, "playingRouteId": g.RouteAB, "effectiveFrom": "2026-01-01"})
	return g
}

func asMaps(v any) []map[string]any {
	var out []map[string]any
	for _, x := range v.([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func userID(t *testing.T, role string) string {
	var id string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = 'role.'||$1||'@matrix.test'`, []any{role}, &id)
	return id
}

// golfBooking books, pays and checks in players on a tee time of day
// through P1 (back office), and returns the booking with its first flight.
func golfBooking(t *testing.T, c *Client, day string, teeTime any, players []map[string]any) (map[string]any, string) {
	t.Helper()
	bk := c.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "non_member", "channel": "back_office", "teeTimeId": teeTime,
		"contactName": "P2 Golf", "contactPhone": "+628120000001", "players": players}).JSON()
	if dec(bk["folio"].(map[string]any)["balance"]).IsPositive() {
		payFolio(t, c, bk)
	}
	return bk, firstFlight(bk)
}

func checkIn(t *testing.T, c *Client, day string, bk map[string]any) {
	t.Helper()
	c.Must(200, "POST", "/api/v1/golf/check-ins", map[string]any{"method": "booking_code", "value": bk["code"], "date": day})
}

// dispatch runs the outbox (P1 events drive the P2 round).
func dispatch(t *testing.T) {
	t.Helper()
	if _, err := inst.App.Dispatcher.DispatchPending(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// backdate moves a flight that teed off now back by d (round timings).
func backdate(t *testing.T, flight string, d time.Duration) {
	t.Helper()
	iv := fmt.Sprintf("%d seconds", int(d.Seconds()))
	fid := mustUUID(flight)
	sysExec(t, inst, `UPDATE golf.flights SET tee_off_at = tee_off_at - $2::interval WHERE id = $1`, fid, iv)
	sysExec(t, inst, `UPDATE golf.caddy_assignments SET assigned_at = assigned_at - $2::interval, started_at = started_at - $2::interval WHERE flight_id = $1`, fid, iv)
	sysExec(t, inst, `UPDATE golf.golf_cart_assignments SET assigned_at = assigned_at - $2::interval, out_at = out_at - $2::interval WHERE flight_id = $1`, fid, iv)
}

// EP-05/06/07/08/10/11 acceptance in one round on P1's booking, check-in and
// starter: caddy rotation & replacement, golf cart inspection lifecycle,
// caddy tablet with an offline half and a device handover, scorecard with
// finalization, correction, Hole-in-One verification & claim, Hall of Fame
// opt-in, caddy fee split, settlement and rating.
func TestP2GolfRound(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	g := setupGolfCourse(t, sa, "MG")
	if len(g.Holes) != 18 || g.Holes[0]["sectionCode"] != "MG-A" || g.Holes[9]["sectionCode"] != "MG-B" || g.Holes[2]["par"].(float64) != 3 ||
		g.Holes[9]["strokeIndex"].(float64) != 2 {
		t.Fatalf("route A+B scorecard template: %v", g.Holes)
	}
	day := clubDay(inst, 22, isWeekday)
	playDay, _ := time.ParseInLocation("2006-01-02", day, clubLoc(inst))
	slots := teeTimes(t, sa, g.Course, day)

	// Caddies with a level and a tablet login; clock-in builds the rotation.
	jr := idOf(sa.Must(201, "POST", "/api/v1/golf/caddy-levels", map[string]any{"code": "JR", "name": "Junior", "rank": 1, "feeAmount": "150000"}))
	sr := idOf(sa.Must(201, "POST", "/api/v1/golf/caddy-levels", map[string]any{"code": "SR", "name": "Senior", "rank": 2, "feeAmount": "200000", "minRounds": 1}))
	c1 := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C023", "name": "Caddy Ayu", "gender": "female"}))
	c2 := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C024", "name": "Caddy Budi", "gender": "male"}))
	c3 := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C025", "name": "Caddy Citra", "gender": "female"}))
	sa.Must(200, "PUT", "/api/v1/golf/caddies/"+c1+"/profile", map[string]any{"levelId": jr, "userId": userID(t, "caddy"), "joinedOn": "2024-01-10"})
	sa.Must(200, "PUT", "/api/v1/golf/caddies/"+c2+"/profile", map[string]any{"levelId": jr})
	sa.Must(200, "PUT", "/api/v1/golf/caddies/"+c3+"/profile", map[string]any{"levelId": jr})
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": c1, "at": rfc(at(playDay, 5, 30))})
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": c2, "at": rfc(at(playDay, 5, 40))})
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": c3, "at": rfc(at(playDay, 5, 45))})
	if r := sa.Do("POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": c1, "at": rfc(at(playDay, 5, 50))}); r.Status != 409 {
		t.Fatalf("double clock-in: %s", r)
	}
	q := sa.Must(200, "GET", "/api/v1/golf/caddy-rotation?date="+day, nil).Items()
	if len(q) != 3 || q[0]["caddyId"] != c1 || q[2]["caddyId"] != c3 {
		t.Fatalf("rotation by arrival: %v", q)
	}

	// Golf carts: only an inspected cart is Ready.
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-checklists", map[string]any{"code": "PREOP", "name": "Pre-op", "inspectionKind": "pre_op", "items": []string{"Brakes", "Battery", "Tyres"}})
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-checklists", map[string]any{"code": "POSTOP", "name": "Post-op", "inspectionKind": "post_op", "items": []string{"Body", "Battery"}})
	b1 := idOf(sa.Must(201, "POST", "/api/v1/golf/golf-carts", map[string]any{"code": "MGB1", "name": "Buggy MG 1"}))
	b2 := idOf(sa.Must(201, "POST", "/api/v1/golf/golf-carts", map[string]any{"code": "MGB2", "name": "Buggy MG 2"}))
	sa.Must(200, "PUT", "/api/v1/golf/golf-carts/"+b1+"/profile", map[string]any{"serviceThresholdHours": "1"})
	ok3 := []map[string]any{{"item": "Brakes", "pass": true}, {"item": "Battery", "pass": true}, {"item": "Tyres", "pass": true}}
	inspect := func(cart string, body map[string]any) Resp {
		body["golfCartId"] = cart
		return sa.Do("POST", "/api/v1/golf/golf-cart-inspections", body)
	}
	if r := inspect(b1, map[string]any{"kind": "pre_op", "results": ok3[:2]}); r.Status != 422 {
		t.Fatalf("incomplete checklist must be refused: %s", r)
	}
	if insp := inspect(b1, map[string]any{"kind": "pre_op", "results": ok3, "batteryPercent": 95}); insp.Status != 201 || insp.JSON()["readinessAfter"] != "ready" {
		t.Fatalf("pre-op pass: %s", insp)
	}
	bad := []map[string]any{{"item": "Brakes", "pass": false, "note": "soft"}, {"item": "Battery", "pass": true}, {"item": "Tyres", "pass": true}}
	if insp := inspect(b2, map[string]any{"kind": "pre_op", "results": bad}).JSON(); insp["readinessAfter"] != "maintenance" || insp["maintenanceId"] == nil {
		t.Fatalf("pre-op fail opens maintenance: %v", insp)
	}

	// Booking (P1): a customer and a walk-in visitor (a P1 guest).
	bk, fid := golfBooking(t, sa, day, slots[0]["id"], []map[string]any{
		{"playerType": "non_member", "customerId": f.CustomerA, "name": "Hendra Wijaya"},
		{"playerType": "non_member", "name": "Tamu Visitor", "phone": "+6281299990000"}})
	players := bk["players"].([]any)
	pA, pV := players[0].(map[string]any), players[1].(map[string]any)
	ca := sa.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "assignments": []map[string]any{
		{"caddyId": c1, "playerIds": []any{pA["id"]}}, {"caddyId": c2, "playerIds": []any{pV["id"]}}}}).Items()[0]
	if r := sa.Do("POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "golfCartIds": []string{b2}}); r.Status != 409 {
		t.Fatalf("a cart in maintenance cannot be assigned: %s", r)
	}
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "golfCartIds": []string{b1}})
	checkIn(t, sa, day, bk)

	// Caddy tablet: My Assignments, accept, round information.
	cad := roleUser(t, inst, "caddy")
	my := cad.Must(200, "GET", "/api/v1/golf/my-assignments", nil).JSON()
	if len(my["next"].([]any)) != 1 {
		t.Fatalf("my assignments: %v", my)
	}
	cad.Must(200, "POST", "/api/v1/golf/caddy-assignments/"+str(ca["id"])+":accept", nil)
	ri := cad.Must(200, "GET", "/api/v1/golf/rounds/"+fid, nil).JSON()
	if len(ri["players"].([]any)) != 2 {
		t.Fatalf("round info: %v", ri)
	}
	cad.Must(201, "POST", "/api/v1/golf/customers/"+f.CustomerA+"/preferences", map[string]any{"category": "beverage", "value": "Es Teh Tawar"})
	other := roleUser(t, inst, "golf_staff")
	if r := other.Do("GET", "/api/v1/golf/my-assignments", nil); r.Status != 403 {
		t.Fatalf("tablet is for caddies: %s", r)
	}

	// Offline: the tablet tees off (P1's starter) and queues holes 1–9; the
	// round is then moved three hours back so the holes fall in the caddy's time.
	teeOff := time.Now().Add(-3 * time.Hour)
	holeAt := func(seq int) time.Time { return teeOff.Add(time.Duration(seq-1) * 12 * time.Minute) }
	sync := func(items []map[string]any) []any {
		return cad.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": items}).JSON()["results"].([]any)
	}
	if res := sync([]map[string]any{{"id": newKey(), "action": "golf.round", "payload": map[string]any{"op": "tee_off", "flightId": fid, "at": rfc(teeOff), "deviceId": "TAB-1"}}}); res[0].(map[string]any)["status"] != "accepted" {
		t.Fatalf("offline tee-off: %v", res)
	}
	backdate(t, fid, 3*time.Hour+time.Minute)
	var items []map[string]any
	for s := 2; s <= 9; s++ {
		items = append(items, map[string]any{"id": newKey(), "action": "golf.round", "payload": map[string]any{"op": "hole", "flightId": fid, "seq": s, "at": rfc(holeAt(s)), "deviceId": "TAB-1"}})
	}
	for _, r := range sync(items) {
		if r.(map[string]any)["status"] != "accepted" {
			t.Fatalf("offline round sync: %v", r)
		}
	}
	// Re-sending the queue (connection dropped before the response) changes nothing.
	for _, r := range sync(items) {
		if r.(map[string]any)["status"] != "duplicate" {
			t.Fatalf("resync must be duplicate: %v", r)
		}
	}
	dispatch(t)
	rd := sa.Must(200, "GET", "/api/v1/golf/rounds/"+fid, nil).JSON()["round"].(map[string]any)
	cards := map[string]string{}
	for _, p := range rd["players"].([]any) {
		pm := p.(map[string]any)
		cards[str(pm["id"])] = str(pm["scorecardId"])
	}
	cardA, cardV := cards[str(pA["id"])], cards[str(pV["id"])]
	if cardA == "" || rd["currentSeq"].(float64) != 9 || rd["status"] != "in_play" {
		t.Fatalf("after offline sync: %v", rd)
	}
	if sc := sa.Must(200, "GET", "/api/v1/golf/scorecards/"+cardV, nil).JSON(); sc["customerId"] == nil {
		t.Fatalf("visitor round must be kept on a customer profile: %v", sc)
	}
	// Scores of holes 1–9 also from the offline queue (two writes of hole 3: the newer wins).
	strokesA := []int{4, 5, 1, 5, 4, 4, 3, 2, 5, 4, 4, 3, 5, 4, 4, 3, 4, 5} // hole 3: HIO (par 3), hole 8: eagle (par 4)
	strokesV := []int{5, 5, 4, 6, 5, 5, 4, 5, 6, 5, 5, 4, 6, 5, 5, 4, 5, 6}
	var scoreItems []map[string]any
	for s := 1; s <= 9; s++ {
		scoreItems = append(scoreItems, map[string]any{"id": newKey(), "action": "golf.round", "payload": map[string]any{"op": "score", "flightId": fid, "scorecardId": cardA,
			"deviceId": "TAB-1", "entries": []map[string]any{{"seq": s, "strokes": strokesA[s-1], "putts": 1, "clientAt": rfc(holeAt(s).Add(10 * time.Minute))}}}})
		scoreItems = append(scoreItems, map[string]any{"id": newKey(), "action": "golf.round", "payload": map[string]any{"op": "score", "flightId": fid, "scorecardId": cardV,
			"deviceId": "TAB-1", "entries": []map[string]any{{"seq": s, "strokes": strokesV[s-1], "clientAt": rfc(holeAt(s).Add(10 * time.Minute))}}}})
	}
	// an older write for hole 3 arriving late must not override the newer one
	scoreItems = append(scoreItems, map[string]any{"id": newKey(), "action": "golf.round", "payload": map[string]any{"op": "score", "flightId": fid, "scorecardId": cardA,
		"entries": []map[string]any{{"seq": 3, "strokes": 3, "clientAt": rfc(holeAt(3))}}}})
	sync(scoreItems)

	// The tablet breaks at hole 10: a replacement tablet continues with holes 1–9 intact.
	ho := cad.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":handover", map[string]any{"deviceId": "TAB-2"}).JSON()
	var sc0 map[string]any
	for _, s := range ho["scorecards"].([]any) {
		if str(s.(map[string]any)["id"]) == cardA {
			sc0 = s.(map[string]any)
		}
	}
	scores := sc0["scores"].([]any)
	if scores[2].(map[string]any)["strokes"].(float64) != 1 || scores[8].(map[string]any)["strokes"].(float64) != 5 || len(ho["times"].(map[string]any)["holes"].([]any)) != 9 {
		t.Fatalf("handover state: %v", sc0)
	}
	// Caddy Replacement at hole 10 (P1); holes 10–18 online.
	sa.Must(200, "POST", "/api/v1/golf/caddy-assignments/"+str(ca["id"])+":replace", map[string]any{"caddyId": c3, "reason": "Caddy unwell"})
	for s := 10; s <= 18; s++ {
		sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":hole-progress", map[string]any{"seq": s, "deviceId": "TAB-2"})
		sa.Must(200, "POST", "/api/v1/golf/scorecards/"+cardA+"/scores", map[string]any{"source": "caddy", "entries": []map[string]any{{"seq": s, "strokes": strokesA[s-1], "putts": 2}}})
		sa.Must(200, "POST", "/api/v1/golf/scorecards/"+cardV+"/scores", map[string]any{"entries": []map[string]any{{"seq": s, "strokes": strokesV[s-1]}}})
	}
	if fin := sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":complete", map[string]any{}).JSON(); fin["status"] != "completed" {
		t.Fatalf("finish: %v", fin)
	}
	dispatch(t)
	// A hole without a pace target uses the default target (15 minutes).
	sysExec(t, inst, `DELETE FROM golf.hole_pace_targets WHERE hole_id = $1`, mustUUID(str(g.Holes[0]["holeId"])))
	times := sa.Must(200, "GET", "/api/v1/golf/rounds/"+fid+"/times", nil).JSON()
	if len(times["holes"].([]any)) != 18 || times["roundMinutes"] == nil {
		t.Fatalf("hole progress without duplicates: %v", times)
	}
	if h := times["holes"].([]any)[0].(map[string]any); h["targetMinutes"].(float64) != 15 {
		t.Fatalf("default pace target: %v", h)
	}

	// Caddy fee of the replaced caddy split by holes served: 9 (c1) and 9 (c3).
	fee := dec(ca["feeAmount"])
	share := func(caddy string) decimal.Decimal {
		var s string
		sysQueryRow(t, inst, `SELECT coalesce(sum(s.share_amount), 0)::text FROM golf.caddy_fee_shares s JOIN golf.caddy_assignments a ON a.id = s.assignment_id
			WHERE a.caddy_id = $1 AND a.flight_id = $2`, []any{mustUUID(caddy), mustUUID(fid)}, &s)
		return dec(s)
	}
	half := fee.Div(decimal.NewFromInt(2))
	// The round_finished event may still be held by the background dispatcher.
	waitFor(t, 10*time.Second, "caddy fee shares", func() bool { dispatch(t); return !share(c1).IsZero() })
	if !share(c1).Equal(half) || !share(c3).Equal(half) {
		t.Fatalf("caddy fee split of %s: c1=%s c3=%s", fee, share(c1), share(c3))
	}

	// Golf cart: returned → post-op fails → maintenance → release → Ready; service alert.
	var alerted bool
	sysQueryRow(t, inst, `SELECT service_alerted_at IS NOT NULL FROM golf.cart_profiles WHERE golf_cart_id = $1`, []any{mustUUID(b1)}, &alerted)
	if !alerted {
		t.Fatal("service alert after the threshold")
	}
	if r := inspect(b1, map[string]any{"kind": "pre_op", "results": ok3}); r.Status != 409 {
		t.Fatalf("a returned cart needs the post-op inspection first: %s", r)
	}
	if insp := inspect(b1, map[string]any{"kind": "post_op", "results": []map[string]any{{"item": "Body", "pass": false, "note": "dent"}, {"item": "Battery", "pass": true}}}).JSON(); insp["readinessAfter"] != "maintenance" {
		t.Fatalf("post-op fail: %v", insp)
	}
	if insp := inspect(b1, map[string]any{"kind": "release", "results": ok3, "batteryPercent": 90}).JSON(); insp["readinessAfter"] != "ready" {
		t.Fatalf("release: %v", insp)
	}
	hist := sa.Must(200, "GET", "/api/v1/golf/golf-carts/"+b1+"/history", nil).JSON()
	if len(hist["maintenance"].([]any)) != 1 || hist["maintenance"].([]any)[0].(map[string]any)["status"] != "closed" {
		t.Fatalf("maintenance closed by release: %v", hist["maintenance"])
	}

	// Scorecard: validate, finalize, immutable, correction with audit.
	sa.Must(200, "POST", "/api/v1/golf/scorecards/"+cardA+":validate", map[string]any{"attestedBy": "Tamu Visitor"})
	final := sa.Must(200, "POST", "/api/v1/golf/scorecards/"+cardA+":finalize", nil).JSON()
	if final["status"] != "finalized" || final["gross"].(float64) != 69 || final["differential"] == nil {
		t.Fatalf("finalized card: %v", final)
	}
	if final["scores"].([]any)[2].(map[string]any)["term"] != "hole_in_one" || final["scores"].([]any)[7].(map[string]any)["term"] != "eagle" {
		t.Fatalf("hole terms: %v", final["scores"])
	}
	if r := sa.Do("POST", "/api/v1/golf/scorecards/"+cardA+"/scores", map[string]any{"entries": []map[string]any{{"seq": 1, "strokes": 3}}}); r.Status != 409 {
		t.Fatalf("finalized card rejects normal edits: %s", r)
	}
	if r := sa.Do("POST", "/api/v1/golf/scorecards/"+cardA+"/corrections", map[string]any{"entries": []map[string]any{{"seq": 1, "strokes": 5}}}); r.Status != 422 {
		t.Fatalf("correction needs a reason: %s", r)
	}
	cor := sa.Must(200, "POST", "/api/v1/golf/scorecards/"+cardA+"/corrections", map[string]any{"reason": "Marker error on hole 1", "entries": []map[string]any{{"seq": 1, "strokes": 5}}}).JSON()
	if cor["gross"].(float64) != 70 {
		t.Fatalf("corrected gross: %v", cor["gross"])
	}
	aud := sa.Must(200, "GET", "/api/v1/golf/scorecards/"+cardA+"/corrections", nil).Items()
	last := aud[len(aud)-1]
	if last["kind"] != "correction" || last["reason"] != "Marker error on hole 1" {
		t.Fatalf("score audit: %v", last)
	}
	// Privacy: staff without view_all who is not the flight's caddy cannot read the card.
	if r := other.Do("GET", "/api/v1/golf/scorecards/"+cardA, nil); r.Status != 403 {
		t.Fatalf("score privacy: %s", r)
	}
	stats := sa.Must(200, "GET", "/api/v1/golf/players/"+f.CustomerA+"/statistics", nil).JSON()
	if stats["rounds"].(float64) != 1 || stats["birdiesOrBetter"].(float64) < 2 {
		t.Fatalf("round statistics: %v", stats)
	}

	// Hole-in-One: draft from the card → verification → Hall of Fame (opt-in) → claim.
	hios := sa.Must(200, "GET", "/api/v1/golf/hole-in-ones?filter[status]=draft", nil).Items()
	if len(hios) != 1 || hios[0]["insured"] != true || hios[0]["caddyId"] != c1 {
		t.Fatalf("HIO draft: %v", hios)
	}
	hid := str(hios[0]["id"])
	if v := sa.Must(200, "POST", "/api/v1/golf/hole-in-ones/"+hid+":verify", map[string]any{"witnesses": []map[string]any{{"name": "Tamu Visitor", "statement": "Saw it drop"}}}).JSON(); v["status"] != "verified" {
		t.Fatalf("HIO verified: %v", v)
	}
	hof := sa.Must(200, "GET", "/api/v1/golf/hall-of-fame?filter[category]=hole_in_one", nil).Items()
	if len(hof) != 1 || hof[0]["consent"] != "pending" {
		t.Fatalf("automatic HOF entry: %v", hof)
	}
	pub := anon(t, inst)
	if es := pub.Must(200, "GET", "/api/v1/public/hall-of-fame?propertyId="+inst.Main.String(), nil).Items(); len(es) != 0 {
		t.Fatalf("no consent: not public: %v", es)
	}
	sa.Must(204, "POST", "/api/v1/golf/hall-of-fame/"+str(hof[0]["id"])+":publish", nil)
	if es := pub.Must(200, "GET", "/api/v1/public/hall-of-fame?propertyId="+inst.Main.String(), nil).Items(); len(es) != 0 {
		t.Fatalf("published but without opt-in stays hidden: %v", es)
	}
	sa.Must(204, "POST", "/api/v1/golf/hall-of-fame/consents", map[string]any{"entryId": hof[0]["id"], "consent": "granted"})
	if k := pub.Must(200, "GET", "/api/v1/public/hall-of-fame/kiosk?propertyId="+inst.Main.String(), nil).JSON(); len(k["entries"].([]any)) != 1 {
		t.Fatalf("kiosk after opt-in: %v", k)
	}
	claim := sa.Must(200, "POST", "/api/v1/golf/hole-in-ones/"+hid+":claim", map[string]any{"submittedOn": clubToday(inst), "providerRef": "INS-77"}).JSON()
	if claim["status"] != "claimed" || len(claim["claimDocuments"].([]any)) != 1 {
		t.Fatalf("claim: %v", claim)
	}
	if done := sa.Must(200, "POST", "/api/v1/golf/hole-in-ones/"+hid+":update-claim", map[string]any{"claimStatus": "paid", "paidAmount": "25000000"}).JSON(); done["status"] != "completed" {
		t.Fatalf("claim paid: %v", done)
	}

	// Non-cash tip (P1), settlement, payout, liability.
	sa.Must(201, "POST", "/api/v1/golf/caddy-tips", map[string]any{"assignmentId": ca["id"], "playerId": pA["id"], "amount": "50000", "method": "non_cash"})
	period := map[string]any{"caddyId": c1, "periodStart": playDay.AddDate(0, 0, -1).Format("2006-01-02"), "periodEnd": playDay.AddDate(0, 0, 1).Format("2006-01-02")}
	st := sa.Must(201, "POST", "/api/v1/golf/caddy-settlements", period).JSON()
	if !dec(st["caddyFee"]).Equal(half) || dec(st["tips"]).String() != "50000" || !dec(st["total"]).Equal(half.Add(decimal.NewFromInt(50000))) ||
		st["status"] != "approved" || st["rounds"].(float64) != 1 {
		t.Fatalf("settlement = caddy fee share + non-cash tips − deductions: %v", st)
	}
	if r := sa.Do("POST", "/api/v1/golf/caddy-settlements", period); r.Status != 409 {
		t.Fatalf("settled lines are not settled twice: %s", r)
	}
	sa.Must(200, "POST", "/api/v1/golf/caddy-settlements/"+str(st["id"])+":pay", map[string]any{"methodType": "bank_transfer", "reference": "TRF-1"})
	for _, l := range sa.Must(200, "GET", "/api/v1/golf/caddy-liabilities", nil).Items() {
		if l["caddyId"] == c1 && !dec(l["liability"]).IsZero() {
			t.Fatalf("liability after payment: %v", l)
		}
	}
	earn := cad.Must(200, "GET", "/api/v1/golf/my-earnings?from="+day+"&to="+day, nil).JSON()
	if !dec(earn["caddyFee"]).Equal(half) || dec(earn["tips"]).String() != "50000" {
		t.Fatalf("tablet earnings: %v", earn)
	}

	// Rating through the post-round feedback link (rates the last caddy too).
	var token string
	sysQueryRow(t, inst, `SELECT token FROM crm.feedback_requests WHERE customer_id = $1 AND context_type = 'round'`, []any{mustUUID(f.CustomerA)}, &token)
	fb := pub.Must(201, "POST", "/api/v1/public/feedback/"+token, map[string]any{"rating": 2, "comment": "Slow on back nine", "subjectRating": 5}).JSON()
	if fb["lowScore"] != true {
		t.Fatalf("low score flagged: %v", fb)
	}
	if r := pub.Do("POST", "/api/v1/public/feedback/"+token, map[string]any{"rating": 5}); r.Status != 409 {
		t.Fatalf("a survey is answered once: %s", r)
	}
	h := sa.Must(200, "GET", "/api/v1/golf/caddies/"+c3+"/history", nil).JSON()
	if dec(h["averageRating"]).String() != "5" || h["rounds"].(float64) != 1 {
		t.Fatalf("caddy history & rating: %v", h)
	}
	// Promotion: indicators shown, level only changes through approval.
	ind := sa.Must(200, "GET", "/api/v1/golf/caddies/"+c1+"/indicators?toLevelId="+sr, nil).JSON()
	if ind["rounds"].(float64) != 1 || ind["eligible"] != true {
		t.Fatalf("indicators: %v", ind)
	}
	lc := sa.Must(201, "POST", "/api/v1/golf/caddies/"+c1+":promote", map[string]any{"toLevelId": sr, "reason": "Good record"}).JSON()
	if lc["status"] != "approved" || lc["toLevel"] != "Senior" {
		t.Fatalf("promotion: %v", lc)
	}
	if cp := sa.Must(200, "GET", "/api/v1/golf/caddies/"+c1+"/profile", nil).JSON(); cp["levelId"] != sr {
		t.Fatalf("level after approval: %v", cp["levelId"])
	}
	if u := sa.Must(200, "GET", "/api/v1/golf/caddy-utilization?from="+day+"&to="+day, nil).Items(); len(u) < 2 {
		t.Fatalf("utilization: %v", u)
	}
	// Golf cart damage incident → approval → charged to the player's folio.
	inc := sa.Must(201, "POST", "/api/v1/golf/golf-cart-incidents", map[string]any{"subjectType": "golf_cart", "golfCartId": b1, "flightId": fid, "playerId": pA["id"],
		"category": "damage", "severity": "medium", "description": "Cracked windscreen", "damageAmount": "750000"}).JSON()
	if got := sa.Must(200, "GET", "/api/v1/golf/golf-cart-incidents?filter[golfCartId]="+b1, nil).Items(); len(got) != 1 || got[0]["damageStatus"] != "charged" {
		t.Fatalf("damage charge: %v (incident %v)", got, inc["incidentNo"])
	}
	// Customer context on the tablet carries the caddy-recorded preference.
	var prefs int
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.customer_preferences WHERE customer_id = $1 AND source = 'caddy'`, []any{mustUUID(f.CustomerA)}, &prefs)
	if prefs != 1 {
		t.Fatalf("caddy preference: %d", prefs)
	}
}

// EP-09 acceptance: a flight past its hole target + tolerance shows as slow
// on the Pace of Play screen; GPS distances to the green.
func TestP2GolfPaceAndRange(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	g := setupGolfCourse(t, sa, "PC")
	day := clubDay(inst, 26, isWeekday)
	bk, fid := golfBooking(t, sa, day, teeTimes(t, sa, g.Course, day)[0]["id"], []map[string]any{
		{"playerType": "non_member", "name": "Slow Player", "phone": "+628120000555"}})
	playDay, _ := time.ParseInLocation("2006-01-02", day, clubLoc(inst))
	caddy := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C031", "name": "Caddy Pace", "gender": "female"}))
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": caddy, "at": rfc(at(playDay, 5, 30))})
	sa.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "auto": true})
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "auto": true})
	checkIn(t, sa, day, bk)
	// The tablet starts the round (tee-off through P1's starter) an hour ago.
	now := time.Now()
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":start", map[string]any{"at": rfc(now.Add(-60 * time.Minute))})
	backdate(t, fid, time.Hour)
	dispatch(t)
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":hole-progress", map[string]any{"seq": 2, "at": rfc(now.Add(-40 * time.Minute))})
	var pace map[string]any
	for _, p := range sa.Must(200, "GET", "/api/v1/golf/pace-of-play", nil).Items() {
		if p["flightId"] == fid {
			pace = p
		}
	}
	if pace == nil || pace["slow"] != true || pace["behindMinutes"].(float64) < 30 {
		t.Fatalf("pace of play: %v", pace)
	}
	n, err := inst.App.Experience.RunPaceCheck(t.Context())
	if err != nil || n < 1 {
		t.Fatalf("pace check job: %d %v", n, err)
	}
	d := sa.Must(200, "GET", fmt.Sprintf("/api/v1/golf/course-maps/%s?lat=%f&lng=106.6", str(g.Holes[0]["holeId"]), -6.2), nil).JSON()["distances"].([]any)
	if len(d) != 4 {
		t.Fatalf("GPS distances: %v", d)
	}
	for _, x := range d {
		if xm := x.(map[string]any); xm["target"] == "green_center" && (xm["meters"].(float64) < 300 || xm["meters"].(float64) > 400) {
			t.Fatalf("distance to the green center: %v", xm)
		}
	}

	// Driving Range: bays, queue, prepaid balls and complimentary buckets.
	sa.Must(201, "POST", "/api/v1/golf/range-bays", map[string]any{"code": "R01", "name": "Bay 1", "area": "outdoor"})
	if bays := sa.Must(200, "GET", "/api/v1/golf/range-bays", nil).Items(); bays[0]["resourceId"] == nil {
		t.Fatalf("bay is a bookable resource: %v", bays)
	}
	s1 := sa.Must(201, "POST", "/api/v1/golf/range-sessions", map[string]any{"customerId": f.CustomerB}).JSON()
	s2 := sa.Must(201, "POST", "/api/v1/golf/range-sessions", map[string]any{"guestName": "Queue Guest"}).JSON()
	if s1["status"] != "active" || s2["status"] != "waiting" || s2["queuePosition"].(float64) != 1 {
		t.Fatalf("bay assignment & queue: %v / %v", s1, s2)
	}
	sa.Must(200, "POST", "/api/v1/golf/range-sessions/"+str(s1["id"])+":end", nil)
	if s := sa.Must(200, "GET", "/api/v1/golf/range-sessions?filter[status]=active", nil).Items(); len(s) != 1 || s[0]["id"] != s2["id"] {
		t.Fatalf("next in queue gets the bay: %v", s)
	}
	balls := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "RNG5000", "name": "Paket 5.000 Bola (range)",
		"kind": "quota", "category": "driving_range_balls", "unit": "ball", "faceValue": "5000", "price": "4600000", "validityMonths": 6, "prepaid": true}))
	cust := customer(t, sa, "RANGE-C", "Range Regular", map[string]any{"phone": "+628120000777"})
	sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": balls, "customerId": cust, "payment": map[string]any{"methodType": "cash"}},
		"Idempotency-Key", newKey())
	key := newKey()
	b := sa.Must(201, "POST", "/api/v1/golf/range-buckets", map[string]any{"customerId": cust, "balls": 50, "source": "prepaid"}, "Idempotency-Key", key).JSON()
	if b["remainingBalance"] != "4950" || b["dispenseMode"] != "manual" || len(str(b["dispenserCode"])) != 6 {
		t.Fatalf("prepaid bucket: %v", b)
	}
	if again := sa.Must(201, "POST", "/api/v1/golf/range-buckets", map[string]any{"customerId": cust, "balls": 50, "source": "prepaid"}, "Idempotency-Key", key).JSON(); again["id"] != b["id"] {
		t.Fatalf("idempotent bucket: %v", again)
	}
	if r := sa.Do("POST", "/api/v1/golf/range-buckets", map[string]any{"balls": 50, "source": "complimentary"}); r.Status != 422 {
		t.Fatalf("complimentary needs a reason: %s", r)
	}
	sa.Must(201, "POST", "/api/v1/golf/range-buckets", map[string]any{"balls": 30, "source": "complimentary", "reason": "Junior clinic"})
	u := sa.Must(200, "GET", "/api/v1/golf/range-usage", nil).JSON()
	if u["balls"].(float64) != 80 || u["ballsBySource"].(map[string]any)["prepaid"].(float64) != 50 || u["sessions"].(float64) < 2 {
		t.Fatalf("range usage: %v", u)
	}
}

// EP-13: reciprocal verification with agreement, card and quota on the
// visit day; the verified visit is linked to P1's reciprocal booking player
// and carries its charge; introduction letters.
func TestP2GolfReciprocal(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	g := setupGolfCourse(t, sa, "RC")
	today := time.Now().In(f.Loc).Format("2006-01-02")
	day := clubDay(inst, 18, isWeekday)
	club := idOf(sa.Must(201, "POST", "/api/v1/golf/reciprocal-clubs", map[string]any{"code": "SGCC", "name": "Singapore Country Club", "country": "Singapore",
		"city": "Singapore", "agreementFrom": "2025-01-01", "agreementTo": "2030-12-31", "visitQuota": 1, "quotaPeriod": "month", "settlementMode": "periodic"}))
	verify := func(body map[string]any) Resp {
		body["clubId"], body["visitDate"] = club, day
		return sa.Do("POST", "/api/v1/golf/reciprocal-visits", body)
	}
	if r := verify(map[string]any{"visitorName": "Lim", "homeCardNo": "SG-1", "cardValidUntil": "2020-01-01", "letterRef": "SG/2026/01"}); r.Status != 409 {
		t.Fatalf("expired home card: %s", r)
	}
	if r := verify(map[string]any{"visitorName": "Lim", "homeCardNo": "SG-1", "cardValidUntil": "2030-01-01"}); r.Status != 422 {
		t.Fatalf("introduction letter required: %s", r)
	}
	vr := verify(map[string]any{"visitorName": "Lim Wei", "email": "lim@sgcc.test", "homeCardNo": "SG-1", "cardValidUntil": "2030-01-01",
		"letterRef": "SG/2026/01", "letterDate": today})
	if vr.Status != 201 {
		t.Fatalf("verify: %s", vr)
	}
	v := vr.JSON()
	if v["verified"] != true || v["settlementStatus"] != "open" {
		t.Fatalf("verified visit: %v", v)
	}
	if r := verify(map[string]any{"visitorName": "Tan", "homeCardNo": "SG-2", "cardValidUntil": "2030-01-01", "letterRef": "SG/2026/02"}); r.Status != 409 {
		t.Fatalf("visit quota per agreement: %s", r)
	}
	// P1 books the reciprocal player; the verified visit is linked to it.
	bk, fid := golfBooking(t, sa, day, teeTimes(t, sa, g.Course, day)[0]["id"], []map[string]any{
		{"playerType": "reciprocal", "customerId": v["customerId"], "name": "Lim Wei", "reciprocalClub": "Singapore Country Club"}})
	player := bk["players"].([]any)[0].(map[string]any)
	linked := sa.Must(200, "POST", "/api/v1/golf/reciprocal-visits/"+str(v["id"])+":link-player", map[string]any{"playerId": player["id"]}).JSON()
	if linked["playerId"] != player["id"] {
		t.Fatalf("linked visit: %v", linked)
	}
	if p := sa.Must(200, "GET", "/api/v1/golf/bookings/"+str(bk["id"]), nil).JSON()["players"].([]any)[0].(map[string]any); p["reciprocalVerified"] != true {
		t.Fatalf("P1 reciprocal player verified by the visit: %v", p)
	}
	playDay, _ := time.ParseInLocation("2006-01-02", day, clubLoc(inst))
	caddy := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C032", "name": "Caddy Reciprocal", "gender": "female"}))
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": caddy, "at": rfc(at(playDay, 5, 30))})
	sa.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "auto": true})
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "auto": true})
	checkIn(t, sa, day, bk)
	visits := sa.Must(200, "GET", "/api/v1/golf/reciprocal-visits?filter[direction]=inbound&from="+day+"&to="+day, nil).Items()
	if len(visits) != 1 || !dec(visits[0]["chargeAmount"]).Equal(dec(player["priceTotal"])) || !dec(player["priceTotal"]).IsPositive() {
		t.Fatalf("reciprocal visits with charges: %v (player price %v)", visits, player["priceTotal"])
	}
	if n := sa.Must(200, "POST", "/api/v1/golf/reciprocal-visits:settle", map[string]any{"visitIds": []any{v["id"]}, "status": "invoiced"}).JSON(); n["count"].(float64) != 1 {
		t.Fatalf("club settlement: %v", n)
	}
	// Introduction letter: only for active golf members; approved → PDF + outbound visit.
	if r := sa.Do("POST", "/api/v1/golf/introduction-letters", map[string]any{"clubId": club, "customerId": f.CustomerB, "playFrom": today, "playTo": today}); r.Status != 409 {
		t.Fatalf("letters are for golf members: %s", r)
	}
	prog := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "GOLF-RC", "name": "Golf (reciprocal test)", "programKind": "golf"}))
	typ, typPkg := membershipType(t, sa, prog, "GOLF-RC-IND", "Golf Individual", map[string]any{"entitlements": map[string]any{"memberRate": true}})
	mem := customer(t, sa, "RC-MEMBER", "Budi Member", map[string]any{"email": "budi@rc.test"})
	activeMembership(t, sa, mem, typ, typPkg, nil)
	l := sa.Must(201, "POST", "/api/v1/golf/introduction-letters", map[string]any{"clubId": club, "customerId": mem, "playFrom": today,
		"playTo": time.Now().AddDate(0, 0, 3).Format("2006-01-02"), "players": 2}).JSON()
	if l["status"] != "issued" || l["fileUrl"] == nil {
		t.Fatalf("introduction letter issued with PDF: %v", l)
	}
	if out := sa.Must(200, "GET", "/api/v1/golf/reciprocal-visits?filter[direction]=outbound", nil).Items(); len(out) != 1 {
		t.Fatalf("outbound visit: %v", out)
	}
	if cl := anon(t, inst).Must(200, "GET", "/api/v1/public/reciprocal-clubs?propertyId="+inst.Main.String(), nil).Items(); len(cl) != 1 || cl[0]["country"] != "Singapore" {
		t.Fatalf("public reciprocal clubs: %v", cl)
	}
}
