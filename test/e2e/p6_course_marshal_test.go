package e2e

import (
	"testing"
	"time"
)

// Course Guide, Course Handicap and the Marshal (PRD P2 FR-PLX-01/04/05,
// roadmap §70 Hole by Hole & Handicap Index): the member sees every hole of
// the demo course with its distances per tee and a course handicap per tee;
// the caddy tablet shows each player's course handicap and the strokes per
// hole; the Course Monitor places the flight on the course map, the Marshal
// warns it and the caddy confirms the message.
func TestCourseGuideAndMarshal(t *testing.T) {
	f := setupP2(t)
	sa := f.SA

	// Member: Hole by Hole of the club's course and the course handicap per tee.
	sa.Must(204, "POST", "/api/v1/golf/players/"+demoMemberCustomer(t, inst)+"/official-handicap", map[string]any{"index": "10.6", "source": "PGI"})
	m := login(t, inst, "member@demo.oneclub.id", demoPassword)
	var g map[string]any // the demo course (other tests add courses of their own)
	for _, x := range m.Must(200, "GET", "/api/v1/member/golf/course-guide", nil).Items() {
		if x["courseId"] == demoCourse(t, inst) {
			g = x
		}
	}
	if g == nil {
		t.Fatal("no course guide of the demo course")
	}
	holes := asMaps(g["holes"])
	if len(holes) != 18 || g["par"].(float64) != 72 || g["handicapIndex"] != "10.6" {
		t.Fatalf("course guide: %d holes, par %v, index %v", len(holes), g["par"], g["handicapIndex"])
	}
	h1 := holes[0]
	if h1["par"].(float64) != 5 || h1["strokeIndex"].(float64) != 15 || h1["distances"].(map[string]any)["BLACK"].(float64) != 478 || str(h1["description"]) == "" {
		t.Fatalf("hole 1 of the club's card: %v", h1)
	}
	tees := asMaps(g["teeSets"])
	if len(tees) != 4 || tees[0]["code"] != "BLACK" || tees[0]["lengthMeters"].(float64) != 6311 {
		t.Fatalf("tee sets, longest first: %v", tees)
	}
	for _, tee := range tees { // the club's Handicap Index tables: 10.6 → 13 from Black (134), Blue and Red (132), 12 from White (130)
		want := map[string]float64{"BLACK": 13, "BLUE": 12, "RED": 12, "WHITE": 12}[str(tee["code"])]
		if tee["courseHandicap"].(float64) != want || tee["courseHandicap9"] == nil {
			t.Fatalf("course handicap from %v: %v (want %v)", tee["code"], tee["courseHandicap"], want)
		}
	}

	// A flight in play on a course with a geo-referenced map.
	c := setupGolfCourse(t, sa, "CM")
	sa.Must(201, "POST", "/api/v1/golf/course-assets", map[string]any{"courseId": c.Course, "code": "CM-OVERVIEW", "assetType": "course_map", "name": "Course map",
		"imageUrl": "https://images.example.com/cm-course-map.jpg", "geometry": map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{106.59, -6.18}, {106.61, -6.18}, {106.61, -6.21}, {106.59, -6.21}, {106.59, -6.18}}}}})
	sa.Must(204, "POST", "/api/v1/golf/players/"+f.CustomerA+"/official-handicap", map[string]any{"index": "14.2", "source": "PGI"})
	day := clubDay(inst, 27, isWeekday)
	bk, fid := golfBooking(t, sa, day, teeTimes(t, sa, c.Course, day)[0]["id"], []map[string]any{
		{"playerType": "non_member", "customerId": f.CustomerA, "name": "Hendra Wijaya"}})
	playDay, _ := time.ParseInLocation("2006-01-02", day, clubLoc(inst))
	caddy := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C041", "name": "Caddy Marshal", "gender": "female"}))
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": caddy, "at": rfc(at(playDay, 5, 30))})
	sa.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "auto": true})
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "auto": true})
	checkIn(t, sa, day, bk)
	now := time.Now()
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":start", map[string]any{"at": rfc(now.Add(-60 * time.Minute))})
	backdate(t, fid, time.Hour)
	// other tests of the shared instance may leave events behind: only this flight matters
	_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":hole-progress", map[string]any{"seq": 3, "at": rfc(now.Add(-30 * time.Minute))})

	// Caddy tablet: course handicap 14.2 × 130 / 113 = 16, spread by stroke index.
	ri := sa.Must(200, "GET", "/api/v1/golf/rounds/"+fid, nil).JSON()
	pl := asMaps(ri["players"])[0]
	strokes := 0
	for _, s := range pl["holeStrokes"].([]any) {
		strokes += int(s.(float64))
	}
	if pl["courseHandicap"] == nil || pl["courseHandicap"].(float64) != 16 || strokes != 16 || len(pl["holeStrokes"].([]any)) != 18 {
		t.Fatalf("tablet course handicap: %v %v", pl["courseHandicap"], pl["holeStrokes"])
	}
	if len(ri["interventions"].([]any)) != 0 {
		t.Fatalf("no marshal message yet: %v", ri["interventions"])
	}

	// Course Monitor: the flight on hole 3 of the map, slow (60 min for 3 × 14).
	mon := sa.Must(200, "GET", "/api/v1/golf/course-monitor?courseId="+c.Course, nil).JSON()
	var fl map[string]any
	for _, x := range asMaps(mon["flights"]) {
		if x["flightId"] == fid {
			fl = x
		}
	}
	if fl == nil || fl["holeNumber"].(float64) != 3 || fl["slow"] != true || fl["mapX"] == nil || fl["mapY"] == nil {
		t.Fatalf("flight on the monitor: %v", fl)
	}
	if mon["mapUrl"] != "https://images.example.com/cm-course-map.jpg" {
		t.Fatalf("course map picture: %v", mon["mapUrl"])
	}
	if mh := asMaps(mon["holes"]); len(mh) != 18 || mh[2]["flights"].(float64) != 1 || mh[0]["mapX"] == nil {
		t.Fatalf("hole markers: %v", mh)
	}
	if x := fl["mapX"].(float64); x < 0.45 || x > 0.55 { // lng 106.6 is the middle of the map
		t.Fatalf("map position: %v", x)
	}

	// Cart View of the caddy tablet: the hole on the course map, with the
	// green and the tablet's GPS position (lng 106.6 = the middle of the map).
	cv := roleUser(t, inst, "caddy").Must(200, "GET", "/api/v1/golf/course-maps/"+str(c.Holes[2]["holeId"])+"?lat=-6.195&lng=106.6", nil).JSON()
	if cv["overviewUrl"] != "https://images.example.com/cm-course-map.jpg" || cv["greenX"] == nil || cv["hereX"] == nil || len(cv["distances"].([]any)) == 0 {
		t.Fatalf("cart view of hole 3: %v", cv)
	}
	if x := cv["hereX"].(float64); x < 0.45 || x > 0.55 {
		t.Fatalf("tablet on the course map: %v", x)
	}

	// Marshal: only the Starter / Marshal (and managers) intervene; a note needs a text.
	roleUser(t, inst, "caddy").Must(403, "POST", "/api/v1/golf/flights/"+fid+"/pace-interventions", map[string]any{"kind": "warning"})
	marshal := roleUser(t, inst, "starter_marshal")
	marshal.Must(422, "POST", "/api/v1/golf/flights/"+fid+"/pace-interventions", map[string]any{"kind": "note"})
	marshal.Must(422, "POST", "/api/v1/golf/flights/"+fid+"/pace-interventions", map[string]any{"kind": "shout"})
	iv := marshal.Must(201, "POST", "/api/v1/golf/flights/"+fid+"/pace-interventions", map[string]any{"kind": "warning"}).JSON()
	if iv["hole"] != "Hole 3" || str(iv["message"]) == "" || iv["behindMinutes"].(float64) < 10 || iv["createdByName"] == nil {
		t.Fatalf("warning: %v", iv)
	}
	// the caddy tablet shows it until the caddy confirms
	ri = sa.Must(200, "GET", "/api/v1/golf/rounds/"+fid, nil).JSON()
	if msgs := asMaps(ri["interventions"]); len(msgs) != 1 || msgs[0]["id"] != iv["id"] {
		t.Fatalf("tablet messages: %v", ri["interventions"])
	}
	ack := sa.Must(200, "POST", "/api/v1/golf/pace-interventions/"+str(iv["id"])+":acknowledge", nil).JSON()
	if ack["acknowledgedAt"] == nil {
		t.Fatalf("acknowledged: %v", ack)
	}
	if msgs := sa.Must(200, "GET", "/api/v1/golf/rounds/"+fid, nil).JSON()["interventions"].([]any); len(msgs) != 0 {
		t.Fatalf("confirmed message leaves the tablet: %v", msgs)
	}
	mon = marshal.Must(200, "GET", "/api/v1/golf/course-monitor?courseId="+c.Course, nil).JSON()
	if log := asMaps(mon["interventions"]); len(log) != 1 || log[0]["acknowledgedAt"] == nil || log[0]["kind"] != "warning" {
		t.Fatalf("today's log: %v", log)
	}

	// A flight off the course takes no intervention.
	sa.Must(200, "POST", "/api/v1/golf/starter-queue/"+fid+":finish", map[string]any{"holesPlayed": 18})
	marshal.Must(409, "POST", "/api/v1/golf/flights/"+fid+"/pace-interventions", map[string]any{"kind": "reminder"})
}
