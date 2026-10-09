package e2e

import (
	"strings"
	"testing"
	"time"
)

// Course maintenance (demo feedback 9 Oct 2026, replaces Smartscore Golf
// O&M): the golf manager plans the day's work per hole (or copies the day
// before), the groundstaff starts it — the hole closes in Course Status and
// shows on the Course Monitor — and finishes it with notes and a photo; the
// hole keeps its maintenance history.
func TestCourseMaintenance(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	c := setupGolfCourse(t, sa, "MT")
	gs := roleUser(t, inst, "golf_staff")
	today := time.Now().In(clubLoc(inst)).Format("2006-01-02")
	tomorrow := time.Now().In(clubLoc(inst)).AddDate(0, 0, 1).Format("2006-01-02")
	h1, h2 := str(c.Holes[0]["holeId"]), str(c.Holes[1]["holeId"])

	planned := sa.Must(201, "POST", "/api/v1/golf/maintenance-tasks:plan", map[string]any{"courseId": c.Course, "workDate": today, "holeIds": []string{h1, h2},
		"area": "green", "taskType": "mowing_green", "plannedStart": "06:00", "assigneeName": "Budi Grounds", "closesHole": true}).Items()
	if len(planned) != 2 || planned[0]["status"] != "planned" || planned[0]["holeLabel"] == nil {
		t.Fatalf("the plan: %v", planned)
	}
	sa.Must(201, "POST", "/api/v1/golf/maintenance-tasks:plan", map[string]any{"courseId": c.Course, "workDate": today, "area": "irrigation", "taskType": "irrigation"})
	sa.Must(422, "POST", "/api/v1/golf/maintenance-tasks:plan", map[string]any{"courseId": c.Course, "workDate": today, "area": "green", "taskType": "dancing"})
	if n := len(sa.Must(201, "POST", "/api/v1/golf/maintenance-tasks:copy-day", map[string]any{"courseId": c.Course, "from": today, "to": tomorrow}).Items()); n != 3 {
		t.Fatalf("copied tasks: %d", n)
	}
	sa.Must(409, "POST", "/api/v1/golf/maintenance-tasks:copy-day", map[string]any{"courseId": c.Course, "from": "2001-01-01", "to": tomorrow})

	board := gs.Must(200, "GET", "/api/v1/golf/maintenance-board?courseId="+c.Course+"&date="+today, nil).JSON()
	if board["planned"].(float64) != 3 {
		t.Fatalf("the day's board: %v", board)
	}
	roleUser(t, inst, "caddy").Must(403, "POST", "/api/v1/golf/maintenance-tasks/"+str(planned[0]["id"])+":start", nil)

	// started: the hole is closed and shows on the Course Monitor
	gs.Must(200, "POST", "/api/v1/golf/maintenance-tasks/"+str(planned[0]["id"])+":start", nil)
	no := str(planned[0]["holeNumber"])
	closed := func() string {
		for _, s := range sa.Must(200, "GET", "/api/v1/golf/course-status", nil).Items() {
			if s["courseId"] == c.Course {
				return str(s["closedHoles"])
			}
		}
		return ""
	}
	if !strings.Contains(","+closed()+",", ","+no+",") {
		t.Fatalf("hole %s closed while worked: %q", no, closed())
	}
	var hole map[string]any
	for _, h := range asMaps(sa.Must(200, "GET", "/api/v1/golf/course-monitor?courseId="+c.Course, nil).JSON()["holes"]) {
		if h["holeId"] == h1 {
			hole = h
		}
	}
	if hole == nil || len(hole["maintenance"].([]any)) != 1 {
		t.Fatalf("maintenance on the Course Monitor: %v", hole)
	}

	// done with notes and a photo: the hole opens again
	done := gs.Must(200, "POST", "/api/v1/golf/maintenance-tasks/"+str(planned[0]["id"])+":complete", map[string]any{"doneNotes": "Cut at 3.2 mm",
		"photoUrl": "https://images.example.com/green-1.jpg"}).JSON()
	if done["status"] != "done" || done["photoUrl"] != "https://images.example.com/green-1.jpg" || done["completedAt"] == nil {
		t.Fatalf("work done: %v", done)
	}
	if strings.Contains(","+closed()+",", ","+no+",") {
		t.Fatalf("hole %s open again: %q", no, closed())
	}
	gs.Must(422, "POST", "/api/v1/golf/maintenance-tasks/"+str(planned[1]["id"])+":complete", map[string]any{"photoUrl": "file:///c/photo.jpg"})
	sa.Must(200, "POST", "/api/v1/golf/maintenance-tasks/"+str(planned[1]["id"])+":cancel", map[string]any{"reason": "Rain"})
	gs.Must(409, "POST", "/api/v1/golf/maintenance-tasks/"+str(planned[1]["id"])+":start", nil)

	// the master: one task edited in the Back Office, a hole of another course refused
	one := sa.Must(201, "POST", "/api/v1/golf/maintenance-tasks", map[string]any{"courseId": c.Course, "holeId": h2, "area": "bunker", "taskType": "bunker",
		"workDate": tomorrow}).JSON()
	sa.Must(200, "PATCH", "/api/v1/golf/maintenance-tasks/"+str(one["id"]), map[string]any{"assigneeName": "Tono Grounds", "plannedStart": "07:30"})
	other := setupGolfCourse(t, sa, "MU")
	sa.Must(422, "PATCH", "/api/v1/golf/maintenance-tasks/"+str(one["id"]), map[string]any{"holeId": other.Holes[0]["holeId"]})
	sa.Must(204, "DELETE", "/api/v1/golf/maintenance-tasks/"+str(one["id"]), nil)

	// history per hole
	if hist := sa.Must(200, "GET", "/api/v1/golf/maintenance-history?holeId="+h1, nil).Items(); len(hist) != 1 || hist[0]["doneNotes"] != "Cut at 3.2 mm" {
		t.Fatalf("history of the hole: %v", hist)
	}
	for _, mc := range gs.Must(200, "GET", "/api/v1/golf/maintenance-courses", nil).Items() {
		if mc["courseId"] != c.Course {
			continue
		}
		for _, h := range asMaps(mc["holes"]) {
			if h["holeId"] == h1 && h["lastDone"].(map[string]any)["mowing_green"] != today {
				t.Fatalf("last mowing of the hole: %v", h)
			}
		}
	}
}
