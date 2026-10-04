package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type golfCourse struct {
	Course, RouteAB, RouteA, TeeSet string
	Holes                           []map[string]any
}

// setupGolfCourse creates the course with sections A and B (9 holes each),
// a tee set and the playing routes A+B (18 holes) and A (9 holes).
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
	g.Holes = c.Must(200, "GET", "/api/v1/golf/playing-routes/"+g.RouteAB+"/holes?teeSetId="+g.TeeSet, nil).Items()
	return g
}

func userID(t *testing.T, role string) string {
	var id string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = 'role.'||$1||'@matrix.test'`, []any{role}, &id)
	return id
}

// EP-05/06/07/08/10/11 acceptance in one round: all-in check-in, caddy
// rotation & replacement, golf cart inspection lifecycle, caddy tablet with
// an offline half and a device handover, scorecard with finalization,
// correction, Hole-in-One verification & claim, Hall of Fame opt-in,
// caddy fee settlement and rating.
func TestP2GolfRound(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	g := setupGolfCourse(t, sa, "MG")
	if len(g.Holes) != 18 || g.Holes[0]["sectionCode"] != "MG-A" || g.Holes[9]["sectionCode"] != "MG-B" || g.Holes[2]["par"].(float64) != 3 ||
		g.Holes[9]["strokeIndex"].(float64) != 2 {
		t.Fatalf("route A+B scorecard template: %v", g.Holes)
	}
	// All-in green fee with a caddy fee (liability) and HIO insurance.
	rule(t, sa, map[string]any{"code": "GF-AB", "name": "Green Fee 18 holes", "serviceType": "golf", "itemRef": "MG-AB", "unit": "pax",
		"price": "1000000", "taxCodes": []string{"P2VAT"}, "revenueComponent": "green_fee", "components": []map[string]any{
			{"code": "green_fee", "name": "Green Fee", "amount": "600000", "revenueComponent": "green_fee"},
			{"code": "caddy_fee", "name": "Caddy Fee", "amount": "200000", "revenueComponent": "caddy_fee", "liability": true},
			{"code": "buggy_fee", "name": "Buggy", "amount": "150000", "revenueComponent": "buggy_fee"},
			{"code": "hio_insurance", "name": "HIO Insurance", "amount": "50000", "revenueComponent": "hio_insurance"}}})

	// Caddies, levels, attendance & rotation.
	jr := idOf(sa.Must(201, "POST", "/api/v1/golf/caddy-levels", map[string]any{"code": "JR", "name": "Junior", "rank": 1, "feeAmount": "150000"}))
	sr := idOf(sa.Must(201, "POST", "/api/v1/golf/caddy-levels", map[string]any{"code": "SR", "name": "Senior", "rank": 2, "feeAmount": "200000", "minRounds": 1}))
	c1 := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C023", "name": "Caddy Ayu", "levelId": jr, "userId": userID(t, "caddy"), "joinedOn": "2024-01-10"}))
	c2 := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C024", "name": "Caddy Budi", "levelId": jr}))
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": c1})
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": c2})
	if r := sa.Do("POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": c1}); r.Status != 409 {
		t.Fatalf("double clock-in: %s", r)
	}
	q := sa.Must(200, "GET", "/api/v1/golf/caddy-rotation", nil).Items()
	if len(q) != 2 || q[0]["caddyId"] != c1 {
		t.Fatalf("rotation by arrival: %v", q)
	}

	// Golf carts: only an inspected cart is Ready.
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-checklists", map[string]any{"code": "PREOP", "name": "Pre-op", "inspectionKind": "pre_op", "items": []string{"Brakes", "Battery", "Tyres"}})
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-checklists", map[string]any{"code": "POSTOP", "name": "Post-op", "inspectionKind": "post_op", "items": []string{"Body", "Battery"}})
	b1 := idOf(sa.Must(201, "POST", "/api/v1/golf/golf-carts", map[string]any{"code": "B01", "name": "Buggy 01", "serviceThresholdHours": "1"}))
	b2 := idOf(sa.Must(201, "POST", "/api/v1/golf/golf-carts", map[string]any{"code": "B02", "name": "Buggy 02"}))
	ok3 := []map[string]any{{"item": "Brakes", "pass": true}, {"item": "Battery", "pass": true}, {"item": "Tyres", "pass": true}}
	if r := sa.Do("POST", "/api/v1/golf/golf-carts/"+b1+"/inspections", map[string]any{"kind": "pre_op", "results": ok3[:2]}); r.Status != 422 {
		t.Fatalf("incomplete checklist must be refused: %s", r)
	}
	if insp := sa.Must(201, "POST", "/api/v1/golf/golf-carts/"+b1+"/inspections", map[string]any{"kind": "pre_op", "results": ok3, "batteryPercent": 95}).JSON(); insp["readinessAfter"] != "ready" {
		t.Fatalf("pre-op pass: %v", insp)
	}
	bad := []map[string]any{{"item": "Brakes", "pass": false, "note": "soft"}, {"item": "Battery", "pass": true}, {"item": "Tyres", "pass": true}}
	if insp := sa.Must(201, "POST", "/api/v1/golf/golf-carts/"+b2+"/inspections", map[string]any{"kind": "pre_op", "results": bad}).JSON(); insp["readinessAfter"] != "maintenance" || insp["maintenanceId"] == nil {
		t.Fatalf("pre-op fail opens maintenance: %v", insp)
	}

	// Flight: a member-guest and a walk-in visitor (customer created, FR-SCR-10).
	now := time.Now()
	tee := now.Add(-4 * time.Hour)
	fl := sa.Must(201, "POST", "/api/v1/golf/flights", map[string]any{"routeId": g.RouteAB, "teeSetId": g.TeeSet, "teeTime": rfc(tee), "players": []map[string]any{
		{"customerId": f.CustomerA, "playerType": "guest_of_member"},
		{"name": "Tamu Visitor", "phone": "+6281299990000", "playerType": "visitor"}}}, "Idempotency-Key", newKey()).JSON()
	fid := str(fl["id"])
	players := fl["players"].([]any)
	pA := players[0].(map[string]any)
	pV := players[1].(map[string]any)
	if pV["customerId"] == nil {
		t.Fatalf("visitor round must be kept on a customer profile: %v", pV)
	}
	ci := sa.Must(200, "POST", "/api/v1/golf/flights/"+fid+":check-in", map[string]any{}).JSON()
	pA = ci["players"].([]any)[0].(map[string]any)
	if pA["status"] != "checked_in" || pA["hioInsured"] != true || pA["folioId"] == nil {
		t.Fatalf("check-in: %v", pA)
	}
	folio := sa.Must(200, "GET", "/api/v1/billing/folios/"+str(pA["folioId"]), nil).JSON()
	sum := map[string]float64{}
	for _, l := range folio["lines"].([]any) {
		lm := l.(map[string]any)
		var v float64
		fmt.Sscan(str(lm["totalAmount"]), &v)
		sum[str(lm["revenueComponent"])] += v
	}
	if sum["caddy_fee"] != 0 || sum["green_fee"]+sum["buggy_fee"]+sum["hio_insurance"] < 799999.9 || sum["green_fee"]+sum["buggy_fee"]+sum["hio_insurance"] > 800000.1 {
		t.Fatalf("all-in check-in lines (caddy fee held for the caddy): %v", sum)
	}
	ca := sa.Must(201, "POST", "/api/v1/golf/flights/"+fid+"/caddies", map[string]any{}).JSON()
	if ca["caddyId"] != c1 || ca["status"] != "assigned" {
		t.Fatalf("next caddy in rotation: %v", ca)
	}
	if r := sa.Do("POST", "/api/v1/golf/flights/"+fid+"/golf-carts", map[string]any{"cartId": b2}); r.Status != 409 {
		t.Fatalf("a cart in maintenance cannot be assigned: %s", r)
	}
	if cart := sa.Must(201, "POST", "/api/v1/golf/flights/"+fid+"/golf-carts", map[string]any{}).JSON(); cart["cartId"] != b1 {
		t.Fatalf("first Ready cart: %v", cart)
	}

	// Caddy tablet: My Assignments, accept, round information.
	cad := roleUser(t, inst, "caddy")
	my := cad.Must(200, "GET", "/api/v1/golf/my-assignments", nil).JSON()
	if len(my["next"].([]any)) != 1 {
		t.Fatalf("my assignments: %v", my)
	}
	cad.Must(200, "POST", "/api/v1/golf/caddy-assignments/"+str(ca["id"])+":accept", nil)
	ri := cad.Must(200, "GET", "/api/v1/golf/rounds/"+fid, nil).JSON()
	if len(ri["holes"].([]any)) != 18 || len(ri["players"].([]any)) != 2 {
		t.Fatalf("round info: %v", ri)
	}
	cad.Must(201, "POST", "/api/v1/golf/customers/"+f.CustomerA+"/preferences", map[string]any{"category": "beverage", "value": "Es Teh Tawar"})
	other := roleUser(t, inst, "golf_staff")
	if r := other.Do("GET", "/api/v1/golf/my-assignments", nil); r.Status != 403 {
		t.Fatalf("tablet is for caddies: %s", r)
	}

	// Offline: tee off and holes 1–9 are queued on the tablet and synced later.
	teeOff := tee.Add(5 * time.Minute)
	holeAt := func(seq int) time.Time { return teeOff.Add(time.Duration(seq-1) * 12 * time.Minute) }
	var items []map[string]any
	items = append(items, map[string]any{"id": newKey(), "action": "golf.round", "payload": map[string]any{"op": "tee_off", "flightId": fid, "at": rfc(teeOff), "deviceId": "TAB-1"}})
	for s := 2; s <= 9; s++ {
		items = append(items, map[string]any{"id": newKey(), "action": "golf.round", "payload": map[string]any{"op": "hole", "flightId": fid, "seq": s, "at": rfc(holeAt(s)), "deviceId": "TAB-1"}})
	}
	res := cad.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": items}).JSON()["results"].([]any)
	for _, r := range res {
		if r.(map[string]any)["status"] != "accepted" {
			t.Fatalf("offline round sync: %v", res)
		}
	}
	// Re-sending the queue (connection dropped before the response) changes nothing.
	for _, r := range cad.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": items}).JSON()["results"].([]any) {
		if r.(map[string]any)["status"] != "duplicate" {
			t.Fatalf("resync must be duplicate: %v", r)
		}
	}
	fd := sa.Must(200, "GET", "/api/v1/golf/flights/"+fid, nil).JSON()
	cards := map[string]string{}
	for _, p := range fd["players"].([]any) {
		pm := p.(map[string]any)
		cards[str(pm["id"])] = str(pm["scorecardId"])
	}
	cardA, cardV := cards[str(pA["id"])], cards[str(pV["id"])]
	if cardA == "" || fd["currentSeq"].(float64) != 9 || fd["status"] != "on_course" {
		t.Fatalf("after offline sync: %v", fd)
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
	cad.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": scoreItems})

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
	// Caddy Replacement at hole 10 (staff); holes 10–18 online from TAB-2.
	sa.Must(200, "POST", "/api/v1/golf/caddy-assignments/"+str(ca["id"])+":replace", map[string]any{"newId": c2, "reason": "Caddy unwell", "atSeq": 10})
	for s := 10; s <= 18; s++ {
		cad.Do("POST", "/api/v1/golf/rounds/"+fid+":hole-progress", map[string]any{"seq": s, "at": rfc(holeAt(s)), "deviceId": "TAB-2"})
		sa.Must(200, "POST", "/api/v1/golf/scorecards/"+cardA+"/scores", map[string]any{"source": "caddy", "entries": []map[string]any{{"seq": s, "strokes": strokesA[s-1], "putts": 2}}})
		sa.Must(200, "POST", "/api/v1/golf/scorecards/"+cardV+"/scores", map[string]any{"entries": []map[string]any{{"seq": s, "strokes": strokesV[s-1]}}})
	}
	fin := sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":complete", map[string]any{"at": rfc(holeAt(18).Add(13 * time.Minute))}).JSON()
	if fin["status"] != "finished" {
		t.Fatalf("finish: %v", fin)
	}
	times := sa.Must(200, "GET", "/api/v1/golf/rounds/"+fid+"/times", nil).JSON()
	if len(times["holes"].([]any)) != 18 || times["roundMinutes"] == nil {
		t.Fatalf("hole progress without duplicates: %v", times)
	}

	// Caddy fee split by holes: each player's 200,000 → 100,000 to each caddy.
	var feeC1, feeC2 string
	sysQueryRow(t, inst, `SELECT trim_scale(coalesce(sum(total_amount) FILTER (WHERE beneficiary_id = $1), 0))::text,
		trim_scale(coalesce(sum(total_amount) FILTER (WHERE beneficiary_id = $2), 0))::text FROM billing.folio_lines WHERE revenue_component = 'caddy_fee'`,
		[]any{mustUUID(c1), mustUUID(c2)}, &feeC1, &feeC2)
	if feeC1 != "200000" || feeC2 != "200000" {
		t.Fatalf("caddy fee split: c1=%s c2=%s", feeC1, feeC2)
	}

	// Golf cart: returned → post-op fails → maintenance → release → Ready; service alert.
	var alerted bool
	sysQueryRow(t, inst, `SELECT service_alerted_at IS NOT NULL FROM golf.golf_carts WHERE id = $1`, []any{mustUUID(b1)}, &alerted)
	board := sa.Must(200, "GET", "/api/v1/golf/golf-carts/board", nil).JSON()
	if board["counts"].(map[string]any)["under_inspection"] == nil || !alerted {
		t.Fatalf("returned cart under inspection + service alert: %v alerted=%v", board["counts"], alerted)
	}
	if r := sa.Do("POST", "/api/v1/golf/golf-carts/"+b1+"/inspections", map[string]any{"kind": "pre_op", "results": ok3}); r.Status != 409 {
		t.Fatalf("a returned cart needs the post-op inspection first: %s", r)
	}
	sa.Must(201, "POST", "/api/v1/golf/golf-carts/"+b1+"/inspections", map[string]any{"kind": "post_op", "results": []map[string]any{{"item": "Body", "pass": false, "note": "dent"}, {"item": "Battery", "pass": true}}})
	fl2 := sa.Must(201, "POST", "/api/v1/golf/flights", map[string]any{"routeId": g.RouteA, "teeTime": rfc(now.Add(2 * time.Hour)), "players": []map[string]any{{"name": "Next Guest", "playerType": "visitor"}}}).JSON()
	if r := sa.Do("POST", "/api/v1/golf/flights/"+str(fl2["id"])+"/golf-carts", map[string]any{"cartId": b1}); r.Status != 409 {
		t.Fatalf("cart failing post-op is not assignable: %s", r)
	}
	if insp := sa.Must(201, "POST", "/api/v1/golf/golf-carts/"+b1+"/inspections", map[string]any{"kind": "release", "results": ok3, "batteryPercent": 90}).JSON(); insp["readinessAfter"] != "ready" {
		t.Fatalf("release: %v", insp)
	}
	hist := sa.Must(200, "GET", "/api/v1/golf/golf-carts/"+b1+"/history", nil).JSON()
	if len(hist["maintenance"].([]any)) != 1 || hist["maintenance"].([]any)[0].(map[string]any)["status"] != "closed" {
		t.Fatalf("maintenance closed by release: %v", hist["maintenance"])
	}

	// Scorecard: submit, finalize, immutable, correction with audit.
	sa.Must(200, "POST", "/api/v1/golf/scorecards/"+cardA+":submit", map[string]any{"attestedBy": "Tamu Visitor"})
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
	if r := sa.Do("POST", "/api/v1/golf/scorecards/"+cardA+":correct", map[string]any{"entries": []map[string]any{{"seq": 1, "strokes": 5}}}); r.Status != 422 {
		t.Fatalf("correction needs a reason: %s", r)
	}
	cor := sa.Must(200, "POST", "/api/v1/golf/scorecards/"+cardA+":correct", map[string]any{"reason": "Marker error on hole 1", "entries": []map[string]any{{"seq": 1, "strokes": 5}}}).JSON()
	if cor["gross"].(float64) != 70 {
		t.Fatalf("corrected gross: %v", cor["gross"])
	}
	aud := sa.Must(200, "GET", "/api/v1/golf/scorecards/"+cardA+"/audit", nil).Items()
	last := aud[len(aud)-1]
	if last["kind"] != "correction" || last["reason"] != "Marker error on hole 1" {
		t.Fatalf("score audit: %v", last)
	}
	// Privacy: another caddy cannot read the card.
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
	if v := sa.Must(200, "POST", "/api/v1/golf/hole-in-ones/"+hid+":submit", map[string]any{"witnesses": []map[string]any{{"name": "Tamu Visitor", "statement": "Saw it drop"}}}).JSON(); v["status"] != "verified" {
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
	sa.Must(204, "POST", "/api/v1/golf/hall-of-fame/"+str(hof[0]["id"])+":consent", map[string]any{"consent": "granted"})
	if k := pub.Must(200, "GET", "/api/v1/public/hall-of-fame/kiosk?propertyId="+inst.Main.String(), nil).JSON(); len(k["entries"].([]any)) != 1 {
		t.Fatalf("kiosk after opt-in: %v", k)
	}
	claim := sa.Must(200, "POST", "/api/v1/golf/hole-in-ones/"+hid+":claim", map[string]any{"submittedOn": time.Now().Format("2006-01-02"), "providerRef": "INS-77"}).JSON()
	if claim["status"] != "claimed" || len(claim["claimDocuments"].([]any)) != 1 {
		t.Fatalf("claim: %v", claim)
	}
	if done := sa.Must(200, "POST", "/api/v1/golf/hole-in-ones/"+hid+":update-claim", map[string]any{"claimStatus": "paid", "paidAmount": "25000000"}).JSON(); done["status"] != "completed" {
		t.Fatalf("claim paid: %v", done)
	}

	// Tip, settlement, payment, liability.
	sa.Must(201, "POST", "/api/v1/golf/caddy-assignments/"+str(ca["id"])+":tip", map[string]any{"amount": "50000", "playerId": pA["id"]}, "Idempotency-Key", newKey())
	st := sa.Must(201, "POST", "/api/v1/golf/caddy-settlements", map[string]any{"caddyId": c1, "periodStart": time.Now().AddDate(0, 0, -1).Format("2006-01-02"),
		"periodEnd": time.Now().AddDate(0, 0, 1).Format("2006-01-02")}).JSON()
	if st["total"] != "250000" || st["caddyFee"] != "200000" || st["tips"] != "50000" || st["status"] != "approved" || st["rounds"].(float64) != 1 {
		t.Fatalf("settlement = caddy fee + non-cash tips − deductions: %v", st)
	}
	if r := sa.Do("POST", "/api/v1/golf/caddy-settlements", map[string]any{"caddyId": c1, "periodStart": time.Now().Format("2006-01-02"), "periodEnd": time.Now().Format("2006-01-02")}); r.Status != 409 {
		t.Fatalf("settled lines are not settled twice: %s", r)
	}
	sa.Must(200, "POST", "/api/v1/golf/caddy-settlements/"+str(st["id"])+":pay", map[string]any{"methodType": "bank_transfer", "reference": "TRF-1"})
	for _, l := range sa.Must(200, "GET", "/api/v1/golf/caddy-liabilities", nil).Items() {
		if l["caddyId"] == c1 && l["liability"] != "0" {
			t.Fatalf("liability after payment: %v", l)
		}
	}
	earn := cad.Must(200, "GET", "/api/v1/golf/my-earnings", nil).JSON()
	if earn["caddyFee"] != "200000" || earn["tips"] != "50000" {
		t.Fatalf("tablet earnings: %v", earn)
	}

	// Rating through the post-round feedback link (rates the caddy too).
	var token string
	sysQueryRow(t, inst, `SELECT token FROM crm.feedback_requests WHERE customer_id = $1 AND context_type = 'round'`, []any{mustUUID(f.CustomerA)}, &token)
	fb := pub.Must(201, "POST", "/api/v1/public/feedback/"+token, map[string]any{"rating": 2, "comment": "Slow on back nine", "subjectRating": 5}).JSON()
	if fb["lowScore"] != true {
		t.Fatalf("low score flagged: %v", fb)
	}
	if r := pub.Do("POST", "/api/v1/public/feedback/"+token, map[string]any{"rating": 5}); r.Status != 409 {
		t.Fatalf("a survey is answered once: %s", r)
	}
	h := sa.Must(200, "GET", "/api/v1/golf/caddies/"+c2+"/history", nil).JSON()
	if h["averageRating"] != "5" || h["rounds"].(float64) != 1 {
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
	if cd := sa.Must(200, "GET", "/api/v1/golf/caddies/"+c1, nil).JSON(); cd["levelId"] != sr {
		t.Fatalf("level after approval: %v", cd["levelId"])
	}
	if u := sa.Must(200, "GET", "/api/v1/golf/caddy-utilization", nil).Items(); len(u) < 2 {
		t.Fatalf("utilization: %v", u)
	}
	// Golf cart damage incident → approval → charged to the player's folio.
	inc := sa.Must(201, "POST", "/api/v1/golf/golf-cart-incidents", map[string]any{"cartId": b1, "flightId": fid, "playerId": pA["id"], "category": "damage",
		"severity": "medium", "description": "Cracked windscreen", "damageAmount": "750000"}).JSON()
	if got := sa.Must(200, "GET", "/api/v1/golf/caddy-incidents?filter[cartId]="+b1, nil).Items(); got[0]["damageStatus"] != "charged" {
		t.Fatalf("damage charge: %v (incident %v)", got, inc["incidentNo"])
	}
	// Customer context on the tablet carries the caddy-recorded preference.
	var prefs int
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.preferences WHERE customer_id = $1 AND source = 'caddy'`, []any{mustUUID(f.CustomerA)}, &prefs)
	if prefs != 1 {
		t.Fatalf("caddy preference: %d", prefs)
	}
	_ = uuid.Nil
}

// EP-09 acceptance: a flight past its hole target + tolerance shows as slow
// on the Pace of Play screen; GPS distances to the green.
func TestP2GolfPaceAndRange(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	g := setupGolfCourse(t, sa, "PC")
	now := time.Now()
	fl := sa.Must(201, "POST", "/api/v1/golf/flights", map[string]any{"routeId": g.RouteAB, "teeTime": rfc(now.Add(-70 * time.Minute)),
		"players": []map[string]any{{"name": "Slow Player", "playerType": "visitor"}}}).JSON()
	fid := str(fl["id"])
	sa.Must(200, "POST", "/api/v1/golf/flights/"+fid+":check-in", map[string]any{"noCharge": true})
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":tee-off", map[string]any{"at": rfc(now.Add(-60 * time.Minute))})
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
	d := sa.Must(200, "GET", fmt.Sprintf("/api/v1/golf/holes/%s/distances?lat=%f&lng=106.6", str(g.Holes[0]["holeId"]), -6.2), nil).Items()
	if len(d) != 4 || d[1]["target"] != "greenCenter" || d[1]["meters"].(float64) < 300 || d[1]["meters"].(float64) > 400 {
		t.Fatalf("GPS distances: %v", d)
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

// EP-13: reciprocal verification with agreement, card and quota; letters.
func TestP2GolfReciprocal(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	g := setupGolfCourse(t, sa, "RC")
	today := time.Now().In(f.Loc).Format("2006-01-02")
	club := idOf(sa.Must(201, "POST", "/api/v1/golf/reciprocal-clubs", map[string]any{"code": "SGCC", "name": "Singapore Country Club", "country": "Singapore",
		"city": "Singapore", "agreementFrom": "2025-01-01", "agreementTo": "2030-12-31", "visitQuota": 1, "quotaPeriod": "month", "rateItem": "RC-AB",
		"settlementMode": "periodic"}))
	if r := sa.Do("POST", "/api/v1/golf/reciprocal-visits:verify", map[string]any{"clubId": club, "visitorName": "Lim", "homeCardNo": "SG-1",
		"cardValidUntil": "2020-01-01", "letterRef": "SG/2026/01"}); r.Status != 409 {
		t.Fatalf("expired home card: %s", r)
	}
	if r := sa.Do("POST", "/api/v1/golf/reciprocal-visits:verify", map[string]any{"clubId": club, "visitorName": "Lim", "homeCardNo": "SG-1",
		"cardValidUntil": "2030-01-01"}); r.Status != 422 {
		t.Fatalf("introduction letter required: %s", r)
	}
	v := sa.Must(201, "POST", "/api/v1/golf/reciprocal-visits:verify", map[string]any{"clubId": club, "visitorName": "Lim Wei", "email": "lim@sgcc.test",
		"homeCardNo": "SG-1", "cardValidUntil": "2030-01-01", "letterRef": "SG/2026/01", "letterDate": today}).JSON()
	if v["verified"] != true || v["settlementStatus"] != "open" {
		t.Fatalf("verified visit: %v", v)
	}
	if r := sa.Do("POST", "/api/v1/golf/reciprocal-visits:verify", map[string]any{"clubId": club, "visitorName": "Tan", "homeCardNo": "SG-2",
		"cardValidUntil": "2030-01-01", "letterRef": "SG/2026/02"}); r.Status != 409 {
		t.Fatalf("visit quota per agreement: %s", r)
	}
	rule(t, sa, map[string]any{"code": "GF-RECIP", "name": "Reciprocal green fee", "serviceType": "golf", "itemRef": "RC-AB", "segment": "reciprocal",
		"unit": "pax", "price": "900000", "revenueComponent": "green_fee"})
	fl := sa.Must(201, "POST", "/api/v1/golf/flights", map[string]any{"routeId": g.RouteAB, "teeTime": rfc(time.Now().Add(time.Hour)),
		"players": []map[string]any{{"customerId": v["customerId"], "reciprocalVisitId": v["id"]}}}).JSON()
	if fl["players"].([]any)[0].(map[string]any)["playerType"] != "reciprocal" {
		t.Fatalf("reciprocal player: %v", fl["players"])
	}
	sa.Must(200, "POST", "/api/v1/golf/flights/"+str(fl["id"])+":check-in", map[string]any{})
	visits := sa.Must(200, "GET", "/api/v1/golf/reciprocal-visits?filter[direction]=inbound", nil).Items()
	if len(visits) != 1 || visits[0]["chargeAmount"] != "900000" {
		t.Fatalf("reciprocal visits with charges: %v", visits)
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
