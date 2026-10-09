package e2e

import (
	"testing"
	"time"
)

// OneClub replaces Smartscore on the golf carts (demo feedback 9 Oct 2026):
// the caddy tablet is the cart's GPS on the Course Monitor, the tablet and
// course control (Marshal, back office) message both ways with read
// receipts, and the live weather of the course comes from Open-Meteo.
func TestCaddyTabletReplacesSmartscore(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	c := setupGolfCourse(t, sa, "SM")
	sa.Must(201, "POST", "/api/v1/golf/course-assets", map[string]any{"courseId": c.Course, "code": "SM-OVERVIEW", "assetType": "course_map", "name": "Course map",
		"imageUrl": "https://images.example.com/sm-course-map.jpg", "geometry": map[string]any{"type": "Polygon",
			"coordinates": [][][]float64{{{106.59, -6.18}, {106.61, -6.18}, {106.61, -6.21}, {106.59, -6.21}, {106.59, -6.18}}}}})
	day := clubDay(inst, 30, isWeekday)
	bk, fid := golfBooking(t, sa, day, teeTimes(t, sa, c.Course, day)[0]["id"], []map[string]any{{"playerType": "non_member", "name": "Sandi Score"}})
	playDay, _ := time.ParseInLocation("2006-01-02", day, clubLoc(inst))
	caddy := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C061", "name": "Caddy Smart", "gender": "female"}))
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": caddy, "at": rfc(at(playDay, 5, 30))})
	sa.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "auto": true})
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "auto": true})
	checkIn(t, sa, day, bk)
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":start", map[string]any{"at": rfc(time.Now().Add(-30 * time.Minute))})

	// the tablet's GPS places its cart and the flight on the course map
	fix := sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+"/position", map[string]any{"lat": -6.2, "lng": 106.605, "accuracy": 12}).JSON()
	if fix["golfCartId"] == nil {
		t.Fatalf("the fix goes to the flight's cart: %v", fix)
	}
	if rough := sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+"/position", map[string]any{"lat": -6.19, "lng": 106.6, "accuracy": 900}).JSON(); rough["golfCartId"] != nil {
		t.Fatalf("a rough fix is ignored: %v", rough)
	}
	mon := sa.Must(200, "GET", "/api/v1/golf/course-monitor?courseId="+c.Course, nil).JSON()
	var cart, fl map[string]any
	for _, x := range asMaps(mon["golfCarts"]) {
		if x["golfCartId"] == fix["golfCartId"] {
			cart = x
		}
	}
	for _, x := range asMaps(mon["flights"]) {
		if x["flightId"] == fid {
			fl = x
		}
	}
	if cart == nil || cart["source"] != "tablet" || cart["lat"].(float64) != -6.2 {
		t.Fatalf("cart on the tablet's GPS: %v", cart)
	}
	if fl == nil || fl["live"] != true || fl["mapX"].(float64) < 0.7 { // lng 106.605 is three quarters to the east
		t.Fatalf("flight placed by GPS: %v", fl)
	}

	// messenger: tablet → course control and back, with read receipts
	sa.Must(201, "POST", "/api/v1/golf/rounds/"+fid+"/messages", map[string]any{"body": "Need a ball spotter"})
	marshal := roleUser(t, inst, "starter_marshal")
	inbox := marshal.Must(200, "GET", "/api/v1/golf/course-messages?courseId="+c.Course, nil).Items()
	if len(inbox) != 1 || inbox[0]["sender"] != "tablet" || inbox[0]["readByCourseAt"] != nil {
		t.Fatalf("course control inbox: %v", inbox)
	}
	if r := marshal.Must(200, "POST", "/api/v1/golf/course-messages:read", map[string]any{"flightId": fid}).Items(); r[0]["readByCourseAt"] == nil {
		t.Fatalf("read by course control: %v", r)
	}
	marshal.Must(201, "POST", "/api/v1/golf/course-messages", map[string]any{"courseId": c.Course, "flightId": fid, "body": "Spotter on the way", "desk": "marshal"})
	all := marshal.Must(201, "POST", "/api/v1/golf/course-messages", map[string]any{"courseId": c.Course, "body": "Lightning nearby: return to the clubhouse", "desk": "office"}).Items()
	if len(all) == 0 || all[0]["broadcast"] != true {
		t.Fatalf("message to every flight: %v", all)
	}
	marshal.Must(422, "POST", "/api/v1/golf/course-messages", map[string]any{"courseId": c.Course, "flightId": fid, "body": " ", "desk": "marshal"})
	roleUser(t, inst, "caddy").Must(403, "POST", "/api/v1/golf/course-messages", map[string]any{"courseId": c.Course, "body": "x", "desk": "marshal"})
	thread := sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+"/messages:read", nil).Items()
	if len(thread) != 3 {
		t.Fatalf("the flight's thread: %v", thread)
	}
	for _, x := range thread {
		if x["sender"] != "tablet" && x["readByTabletAt"] == nil {
			t.Fatalf("read on the tablet: %v", x)
		}
	}

	// live weather at the middle of the course map (Open-Meteo; offline runs say so)
	w := sa.Must(200, "GET", "/api/v1/golf/courses/"+c.Course+"/weather", nil).JSON()
	if w["lat"].(float64) != -6.195 || w["lng"].(float64) != 106.6 {
		t.Fatalf("weather position: %v", w)
	}
	if w["available"] == true && (str(w["summary"]) == "" || str(w["suggestedStatus"]) == "") {
		t.Fatalf("weather: %v", w)
	}
}
