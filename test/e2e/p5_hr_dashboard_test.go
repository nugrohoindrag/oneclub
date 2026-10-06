package e2e

// HR Dashboard (HRIS → Dashboard): headcount and departments, workforce
// coverage of the day against the staffing requirements, the HR queues and
// the sections hidden by permission.

import (
	"strings"
	"testing"
)

func TestP5HRDashboard(t *testing.T) {
	tm := ttSetup(t, [3]string{"department_head", "kitchen_staff", "pos_staff"})
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	today := hrDay(0)
	shift := ttTemplate(t, hr, "DB"+tm.sfx, "08:00", "17:00", "")
	hr.Must(201, "POST", hrBase+"/staffing-requirements", map[string]any{"orgUnitId": tm.unitID, "positionId": tm.posID, "shiftTemplateId": shift,
		"minStaff": 3}, "Idempotency-Key", newKey())
	ttPublishedDays(t, hr, tm, shift, today, today, tm.aID)
	tm.b.Must(201, "POST", "/api/v1/ess/profile-changes", map[string]any{"changes": map[string]any{"phone": "+6281299990077"}})

	d := hr.Must(200, "GET", hrBase+"/dashboard", nil).JSON()
	if !strings.HasPrefix(str(d["date"]), today) {
		t.Fatalf("date: %v", d["date"])
	}
	hc := d["headcount"].(map[string]any)
	if hc["total"].(float64) < 3 {
		t.Fatalf("headcount: %v", hc)
	}
	var unit map[string]any
	for _, x := range d["departments"].([]any) {
		if u := x.(map[string]any); u["orgUnitId"] == tm.unitID {
			unit = u
		}
	}
	if unit == nil || unit["headcount"] != float64(3) {
		t.Fatalf("departments: %v", d["departments"])
	}
	// one of three required shifts is assigned: gap 2
	w := d["workforce"].(map[string]any)
	var cov map[string]any
	for _, x := range w["units"].([]any) {
		if u := x.(map[string]any); u["orgUnitId"] == tm.unitID {
			cov = u
		}
	}
	if cov == nil || cov["required"] != float64(3) || cov["scheduled"] != float64(1) || cov["gap"] != float64(2) || w["gap"].(float64) < 2 {
		t.Fatalf("workforce: %v", w)
	}
	if d["attendance"] == nil {
		t.Fatal("attendance section for HR")
	}
	queues := map[string]float64{}
	for _, x := range d["attention"].([]any) {
		a := x.(map[string]any)
		queues[str(a["key"])] = a["count"].(float64)
	}
	if queues["profile_changes"] < 1 {
		t.Fatalf("attention: %v", d["attention"])
	}

	// sections follow the permissions of the screens they open: the Finance
	// Manager (a department head) sees attendance but not personal data changes
	fm := roleUser(t, inst, "finance_manager").Must(200, "GET", hrBase+"/dashboard", nil).JSON()
	if fm["attendance"] == nil {
		t.Fatalf("finance manager sees: %v", fm)
	}
	for _, x := range fm["attention"].([]any) {
		if k := str(x.(map[string]any)["key"]); k == "profile_changes" {
			t.Fatalf("finance manager queue %s", k)
		}
	}
	tm.a.Must(403, "GET", hrBase+"/dashboard", nil)
}
