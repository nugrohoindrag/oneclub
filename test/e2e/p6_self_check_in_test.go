package e2e

import (
	"testing"
	"time"
)

// Self check-in (demo feedback 9 Oct 2026): the member checks in from the
// Member App on the day of play once the phone is at the club (GPS inside
// the course map plus a margin), or a golfer scans the booking QR at the
// kiosk; the booking then waits at the front desk for caddy and golf cart.
func TestSelfCheckIn(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	fd := login(t, inst, "front.desk@demo.oneclub.id", demoPassword)
	m := login(t, inst, "member@demo.oneclub.id", demoPassword)
	c := setupGolfCourse(t, sa, "SC")
	sa.Must(201, "POST", "/api/v1/golf/course-assets", map[string]any{"courseId": c.Course, "code": "SC-OVERVIEW", "assetType": "course_map", "name": "Course map",
		"imageUrl": "https://images.example.com/sc-course-map.jpg", "geometry": map[string]any{"type": "Polygon",
			"coordinates": [][][]float64{{{106.59, -6.18}, {106.61, -6.18}, {106.61, -6.21}, {106.59, -6.21}, {106.59, -6.18}}}}})
	day := clubDay(inst, 29, isWeekday)
	slots := teeTimes(t, sa, c.Course, day)
	bk := sa.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "member", "channel": "back_office", "teeTimeId": slots[0]["id"],
		"players": []map[string]any{{"playerType": "member", "memberNo": "D0001"}, {"playerType": "non_member", "name": "Sari Guest"}}}).JSON()
	path := "/api/v1/member/bookings/" + str(bk["id"]) + ":self-check-in"
	atClub := map[string]any{"latitude": -6.195, "longitude": 106.6, "accuracy": 20}

	m.Must(409, "POST", path, atClub) // opens on the day of play
	today := time.Now().In(clubLoc(inst)).Format("2006-01-02")
	sysExec(t, inst, `UPDATE golf.bookings SET play_date = $2::date WHERE id = $1`, mustUUID(str(bk["id"])), today)
	m.Must(409, "POST", path, map[string]any{"latitude": -6.2, "longitude": 106.85, "accuracy": 20}) // ~25 km away
	m.Must(422, "POST", path, map[string]any{"latitude": -6.195, "longitude": 106.6, "accuracy": 1500})
	res := m.Must(200, "POST", path, atClub).JSON()
	if n := len(res["checkedIn"].([]any)); n != 1 {
		t.Fatalf("the member checks in themselves only: %v", res)
	}
	m.Must(409, "POST", path, atClub)

	// the front desk sees it waiting for caddy and golf cart
	var w map[string]any
	for _, x := range fd.Must(200, "GET", "/api/v1/golf/self-check-ins?date="+today, nil).Items() {
		if x["bookingId"] == bk["id"] {
			w = x
		}
	}
	if w == nil || w["method"] != "self_app" || w["waitingCaddy"].(float64) != 1 || w["waitingCart"] != true {
		t.Fatalf("self check-in waiting at the desk: %v", w)
	}

	// the caddy clocks in on the tablet (once a day)
	caddy := roleUser(t, inst, "caddy")
	if caddy.Do("GET", "/api/v1/golf/my-assignments", nil).Status == 404 { // no caddy linked to the user yet (run alone)
		cid := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C071", "name": "Caddy Clock", "gender": "female"}))
		sa.Must(200, "PUT", "/api/v1/golf/caddies/"+cid+"/profile", map[string]any{"userId": userID(t, "caddy"), "joinedOn": "2024-01-10"})
	}
	caddy.Must(201, "POST", "/api/v1/golf/my-attendance:clock-in", nil)
	caddy.Must(409, "POST", "/api/v1/golf/my-attendance:clock-in", nil) // once a day

	// kiosk: the booking QR scanned by the golfer
	bk2 := sa.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "non_member", "channel": "back_office", "teeTimeId": slots[1]["id"],
		"contactName": "Kiki Kiosk", "contactPhone": "+628129990095", "players": []map[string]any{{"playerType": "non_member", "name": "Kiki Kiosk"},
			{"playerType": "non_member", "name": "Lala Kiosk"}}}).JSON()
	sysExec(t, inst, `UPDATE golf.bookings SET play_date = $2::date WHERE id = $1`, mustUUID(str(bk2["id"])), today)
	ci := fd.Must(200, "POST", "/api/v1/golf/check-ins", map[string]any{"method": "booking_qr", "value": "oneclub:booking:" + str(bk2["qrToken"]),
		"bookingId": bk2["id"], "kiosk": true}).JSON()
	for _, p := range ci["booking"].(map[string]any)["players"].([]any) {
		if p.(map[string]any)["checkInMethod"] != "kiosk" {
			t.Fatalf("kiosk check-in method: %v", p)
		}
	}
}
