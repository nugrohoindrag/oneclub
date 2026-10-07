package app

// Trial dataset: the course guide and course map of the demo club. At
// go-live the golf admin uploads the course map (geo-referenced by its
// corners) and the hole cards of www.moderngolf.co.id, and marks every hole
// on the map; the Member App Course Guide, the caddy tablet map and the
// Marshal's Course Monitor read them (PRD P2 FR-PLX-01/04/05).

import (
	"context"
	"fmt"

	"oneclub/internal/app/coursephotos"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "course-guide", Order: 19, Setup: trialCourseSetup})
}

// trialCourseBounds are the corners of the course map (overview.jpg,
// 793 × 319 px) around the club in Tangerang: west, east, north, south.
var trialCourseBounds = [4]float64{106.62915, 106.65085, -6.19963, -6.20837}

// trialHoleMarks are the hole numbers on the course map, in pixels.
var trialHoleMarks = [18][2]float64{
	{452, 180}, {525, 63}, {655, 68}, {729, 50}, {698, 177}, {598, 291}, {557, 256}, {616, 128}, {484, 137},
	{366, 234}, {329, 130}, {253, 142}, {148, 191}, {75, 93}, {251, 96}, {366, 101}, {442, 26}, {394, 95},
}

// trialMapPoint turns a pixel of the course map into [lng, lat].
func trialMapPoint(x, y float64) []float64 {
	w, e, n, s := trialCourseBounds[0], trialCourseBounds[1], trialCourseBounds[2], trialCourseBounds[3]
	return []float64{w + x/793*(e-w), n - y/319*(n-s)}
}

func trialCourseSetup(_ context.Context, t *Trial) error {
	gm := t.As(trialGolfManager)
	course := t.course()
	for _, a := range gm.Items("/api/v1/golf/course-assets?filter[courseId]=" + course + "&limit=10") {
		if a.S("code") == "OVERVIEW" {
			return nil // uploaded by hand already
		}
	}
	upload := func(name string) string {
		data, err := coursephotos.FS.ReadFile(name)
		if err != nil {
			t.fail("course picture %s: %v", name, err)
		}
		return gm.Upload("/api/v1/platform/images?resource=golf.course_asset", name, data).S("url")
	}
	w, e, n, s := trialCourseBounds[0], trialCourseBounds[1], trialCourseBounds[2], trialCourseBounds[3]
	gm.Post("/api/v1/golf/course-assets", J{"courseId": course, "code": "OVERVIEW", "assetType": "course_map", "name": "Course map",
		"imageUrl": upload("overview.jpg"), "geometry": J{"type": "Polygon", "coordinates": [][][]float64{{{w, n}, {e, n}, {e, s}, {w, s}, {w, n}}}}})
	holes := map[int]string{}
	for _, h := range gm.Items("/api/v1/golf/holes?filter[courseId]=" + course + "&limit=50") {
		holes[int(h.N("number"))] = h.S("id")
	}
	for i, p := range trialHoleMarks {
		no := i + 1
		hid := holes[no]
		if hid == "" {
			continue
		}
		gm.Post("/api/v1/golf/course-assets", J{"courseId": course, "holeId": hid, "code": fmt.Sprintf("H%02d-LAYOUT", no), "assetType": "course_map",
			"name": fmt.Sprintf("Hole %d layout", no), "imageUrl": upload(fmt.Sprintf("hole-%02d.jpg", no))})
		gm.Post("/api/v1/golf/course-assets", J{"courseId": course, "holeId": hid, "code": fmt.Sprintf("H%02d-GREEN", no), "assetType": "green_center",
			"name": fmt.Sprintf("Hole %d green", no), "geometry": J{"type": "Point", "coordinates": trialMapPoint(p[0], p[1])}})
	}
	return nil
}
