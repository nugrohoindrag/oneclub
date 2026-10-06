package app

// Trial dataset: the PRD P5 areas already live (gap closure, P5 trial
// data). The configuration seed (seed-demo) gives HRIS, advanced CRM and BI
// their master data and the last four weeks of rosters and attendance; the
// trial history adds what a club running OneClub for three months has:
//
//   - hr (Order 45): for the weeks before the demo rosters, the department
//     heads' weekly Shift Schedules of F&B, Kitchen, Sport Club and Golf
//     Operations (copied from the club's roster pattern and published),
//     the staff clocking in and out on the face recognition devices of
//     their area (mostly on time, some late, a few absent), overtime worked
//     after the shift requested and approved, the daily attendance
//     finalisation (the 00:20 job), and the partner caddies enrolled on the
//     caddy house device clocking in every morning (FR-ATT-08).
//   - crm-advanced (Order 75, after engagement & loyalty): the journeys run
//     every morning (renewal, birthday, welcome … steps), the RFM / VIP
//     analytics refresh every Monday and the periodic tier evaluation on the
//     first day of a month; once more at the current date.
//   - bi (Order 950, after the day close): the analytics store is refreshed
//     over the whole history at the current date, so the Executive Overview,
//     the KPI targets and the report builder show the trial months.
//
// Everything goes through the API as the demo HR Manager, Caddy Master and
// Property Admin; the daily attendance job runs from the seeder at its
// simulated time (like the loyalty night job of the engagement seeder).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "hr", Order: 45, Setup: trialHRSetup, Day: trialHRDay})
	registerTrialSeeder(trialSeeder{Name: "crm-advanced", Order: 75, Day: trialCRMAdvancedDay, Final: trialCRMAdvancedFinal})
	registerTrialSeeder(trialSeeder{Name: "bi", Order: 950, Final: trialBIFinal})
}

const trialHR = "hr@demo.oneclub.id"

// trialRosterUnits are the shift departments of the demo rosters.
var trialRosterUnits = []string{"FNB", "KITCHEN", "SPORT", "GOLF-OPS"}

// trialHRCover is the first date of the demo rosters: the trial adds the
// weeks before it (cached in refs; read again after a resumed run).
func trialHRCover(t *Trial) (string, map[string]string) {
	src := map[string]string{}
	if v := t.Ref("hr:cover"); v != "" {
		for _, u := range trialRosterUnits {
			src[u] = t.Ref("hr:src:" + u)
		}
		return v, src
	}
	hr := t.As(trialHR)
	units := t.ids("orgunit", "/api/v1/hris/org-units?limit=500", trialHR)
	cover := ""
	for _, u := range trialRosterUnits {
		uid := units(u)
		if uid == "" {
			continue
		}
		var first J
		for _, s := range hr.Items("/api/v1/hris/schedules?orgUnitId=" + uid + "&status=published&from=" + t.Start.AddDate(0, 0, -14).Format(time.DateOnly) +
			"&to=" + t.Today.AddDate(0, 0, 14).Format(time.DateOnly)) {
			if strings.HasPrefix(s.S("name"), "TRL") {
				continue
			}
			if first == nil || s.S("periodStart") < first.S("periodStart") {
				first = s
			}
		}
		if first == nil {
			continue
		}
		src[u] = first.S("id")
		t.SetRef("hr:src:"+u, first.S("id"))
		if d := first.S("periodStart")[:10]; cover == "" || d < cover {
			cover = d
		}
	}
	if cover == "" {
		cover = t.Start.Format(time.DateOnly) // no demo roster: nothing to extend
	}
	t.SetRef("hr:cover", cover)
	return cover, src
}

func trialHRSetup(_ context.Context, t *Trial) error {
	hr := t.As(trialHR)
	trialHRCover(t)
	// the employee list (no biometrics) reaches the face recognition devices
	for _, d := range hr.Items("/api/v1/hris/attendance-devices?limit=200") {
		if d.S("deviceKind") == "biometric" && d.S("status") == "active" {
			hr.Post("/api/v1/hris/attendance-devices/"+d.S("id")+":sync-employees", J{})
		}
	}
	// FR-ATT-08: the caddy master enrols the senior caddies on the caddy house device with their written consent
	cm := t.As(trialCaddyMaster)
	caddies := cm.Items("/api/v1/golf/caddies?limit=100")
	sort.Slice(caddies, func(i, j int) bool { return caddies[i].S("code") < caddies[j].S("code") })
	enrolled := 0
	for _, c := range caddies {
		if enrolled == 6 {
			break
		}
		if c.S("status") != "" && c.S("status") != "active" {
			continue
		}
		if _, ok := cm.Try("POST", "/api/v1/hris/partner-attendance-profiles", J{"holderKind": "caddy", "partnerId": c.S("id"), "consent": true,
			"signedOn": t.Start.AddDate(0, 0, -1).Format(time.DateOnly)}); ok {
			enrolled++
		}
	}
	if d := t.ids("attdev", "/api/v1/hris/attendance-devices?limit=200", trialHR)("BIO-CADDY"); d != "" {
		hr.Post("/api/v1/hris/attendance-devices/"+d+":sync-employees", J{})
	}
	return nil
}

// trialClock is one planned clock event of a simulated day.
type trialClock struct {
	at         time.Time
	employee   string
	direction  string
	device     string
	overtimeTo time.Time // a clock-out after overtime: request the overtime
	shiftEnd   time.Time
}

func trialHRDay(ctx context.Context, t *Trial, day time.Time) error {
	ds := day.Format(time.DateOnly)
	cover, src := trialHRCover(t)
	// the daily attendance job (00:20) closes yesterday: absences, missing clock-outs
	t.At(day, "00:20")
	if _, err := t.App.Time.Module.RunDaily(ctx); err != nil {
		return err
	}
	if ds >= cover {
		return nil // the demo rosters and attendance cover the last weeks
	}
	hr := t.As(trialHR)
	devices := t.ids("attdev", "/api/v1/hris/attendance-devices?limit=200", trialHR)
	units := t.ids("orgunit", "/api/v1/hris/org-units?limit=500", trialHR)
	// Monday (or the first day): the weekly rosters, copied from the club's pattern and published
	if day.Weekday() == time.Monday || day.Equal(t.Start) {
		t.At(day, "06:30")
		end := day
		for end.Weekday() != time.Sunday && end.AddDate(0, 0, 1).Format(time.DateOnly) < cover {
			end = end.AddDate(0, 0, 1)
		}
		for _, u := range trialRosterUnits {
			if units(u) == "" || src[u] == "" {
				continue
			}
			// copy week from the club's roster pattern, then publish (FR-SCH-02, FR-SCH-05)
			s, ok := hr.Try("POST", "/api/v1/hris/schedules", J{"orgUnitId": units(u), "periodStart": ds, "periodEnd": end.Format(time.DateOnly),
				"name": "TRL " + u + " " + ds, "copyFromScheduleId": src[u], "notes": "Weekly roster " + trialDayTag(day)})
			if ok {
				hr.Try("POST", "/api/v1/hris/schedules/"+s.S("id")+":publish", J{})
			}
		}
	}
	// the day: clock-ins and clock-outs on the device of the area
	r := t.Rand("hr:" + ds)
	var plan []trialClock
	for _, d := range hr.Items("/api/v1/hris/attendance-days?limit=500&from=" + ds + "&to=" + ds) {
		if d.S("scheduledStart") == "" || d.S("status") != "scheduled" {
			continue
		}
		st, err1 := time.Parse(time.RFC3339, d.S("scheduledStart"))
		en, err2 := time.Parse(time.RFC3339, d.S("scheduledEnd"))
		if err1 != nil || err2 != nil {
			continue
		}
		roll := r.IntN(100)
		if roll < 4 {
			continue // absent
		}
		in := st.Add(-time.Duration(2+r.IntN(11)) * time.Minute)
		if roll < 16 {
			in = st.Add(time.Duration(5+r.IntN(21)) * time.Minute) // late
		}
		out := en.Add(time.Duration(r.IntN(13)) * time.Minute)
		c := trialClock{at: out, employee: d.S("employeeId"), direction: "out", shiftEnd: en}
		if roll >= 90 {
			c.at = en.Add(time.Duration(60*(1+r.IntN(2))+r.IntN(6)) * time.Minute) // overtime after the shift
			c.overtimeTo = en.Add(time.Duration(60*(1+r.IntN(2))) * time.Minute)
			if c.overtimeTo.After(c.at) {
				c.overtimeTo = c.at.Truncate(time.Hour)
			}
		}
		dev := devices("BIO-CLUB")
		switch unit := strings.ToLower(d.S("orgUnitName")); {
		case strings.Contains(unit, "sport"):
			dev = devices("BIO-SPORT")
		case strings.Contains(unit, "golf"):
			dev = devices("BIO-CADDY")
		case strings.Contains(unit, "food"), strings.Contains(unit, "kitchen"), strings.Contains(unit, "f&b"):
			dev = devices("BIO-FNB")
		}
		c.device = dev
		plan = append(plan, trialClock{at: in, employee: d.S("employeeId"), direction: "in", device: dev}, c)
	}
	// partner caddies at the caddy house device, 06:00–06:50
	cm := t.As(trialCaddyMaster)
	caddyDev := devices("BIO-CADDY")
	var partners []J
	if caddyDev != "" {
		partners = cm.Items("/api/v1/hris/partner-attendance-profiles?holderKind=caddy")
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].at.Before(plan[j].at) })
	// post the events in 10-minute windows at their simulated time (never "offline")
	post := func(c trialClock) {
		method := []string{"face_recognition", "face_recognition", "fingerprint"}[int(c.at.Unix()/60)%3]
		body := J{"employeeId": c.employee, "method": method, "direction": c.direction, "occurredAt": c.at.UTC().Format(time.RFC3339),
			"eventId": fmt.Sprintf("TRL-%s-%s-%s", ds, c.employee[:8], c.direction)}
		if c.device == "" {
			hr.Post("/api/v1/hris/attendance:clock", J{"employeeId": c.employee, "direction": c.direction, "occurredAt": body["occurredAt"], "note": "Trial clock"})
		} else if _, ok := hr.Try("POST", "/api/v1/hris/attendance-devices/"+c.device+":simulate", body); !ok {
			// no biometric consent on file: the supervisor records it
			hr.Try("POST", "/api/v1/hris/attendance:clock", J{"employeeId": c.employee, "direction": c.direction, "occurredAt": body["occurredAt"],
				"note": "Recorded by the supervisor"})
		}
		if !c.overtimeTo.IsZero() && c.overtimeTo.After(c.shiftEnd) {
			ot, ok := hr.Try("POST", "/api/v1/hris/overtime-requests", J{"employeeId": c.employee, "workDate": ds,
				"startTime": c.shiftEnd.In(t.Loc).Format("15:04"), "endTime": c.overtimeTo.In(t.Loc).Format("15:04"), "reason": "Busy service " + trialDayTag(day)})
			if ok && ot.S("status") == "submitted" {
				hr.Try("POST", "/api/v1/hris/overtime-requests/"+ot.S("id")+":approve", J{})
			}
		}
	}
	window := func(at time.Time) time.Time { return at.Truncate(10 * time.Minute).Add(10 * time.Minute) }
	if len(partners) > 0 {
		pr := t.Rand("hr-caddy:" + ds)
		t.At(day, "06:50")
		for _, p := range partners {
			if pr.IntN(10) < 2 {
				continue // off today
			}
			at := t.Clock(day, "06:00").Add(time.Duration(pr.IntN(45)) * time.Minute)
			cm.Try("POST", "/api/v1/hris/attendance-devices/"+caddyDev+":simulate-partner", J{"profileId": p.S("id"), "method": "face_recognition",
				"direction": "in", "occurredAt": at.UTC().Format(time.RFC3339), "eventId": "TRL-" + ds + "-" + p.S("deviceUserNo")})
		}
	}
	for i := 0; i < len(plan); {
		w := window(plan[i].at)
		j := i
		for j < len(plan) && plan[j].at.Before(w) {
			j++
		}
		batch := plan[i:j]
		lw := w.In(t.Loc)
		mins := int(lw.Sub(day).Minutes())
		t.At(day, fmt.Sprintf("%02d:%02d", mins/60, mins%60))
		t.Parallel(len(batch), 4, func(k int) { post(batch[k]) })
		i = j
	}
	return nil
}

func trialCRMAdvancedDay(_ context.Context, t *Trial, day time.Time) error {
	admin := t.Admin()
	t.At(day, "10:00")
	// the journey steps due this morning (the 15-minute job of a running instance)
	admin.Post("/api/v1/crm/journeys:run", nil)
	if day.Weekday() == time.Monday {
		admin.Post("/api/v1/crm/analytics:refresh", nil)
	}
	if day.Day() == 1 {
		admin.Post("/api/v1/crm/loyalty/tier-evaluations", J{"kind": "periodic"})
	}
	return nil
}

func trialCRMAdvancedFinal(_ context.Context, t *Trial) error {
	admin := t.Admin()
	admin.Post("/api/v1/crm/journeys:run", nil)
	admin.Post("/api/v1/crm/analytics:refresh", nil)
	admin.Post("/api/v1/crm/loyalty/tier-evaluations", J{"kind": "periodic"})
	return nil
}

// trialBIFinal materialises the analytics store over the history, a month
// at a time (BI Policies backfill limit).
func trialBIFinal(_ context.Context, t *Trial) error {
	admin := t.Admin()
	for from := t.Start; !from.After(t.Today); from = from.AddDate(0, 0, 31) {
		to := from.AddDate(0, 0, 30)
		if to.After(t.Today) {
			to = t.Today
		}
		admin.Post("/api/v1/reporting/analytics:refresh", J{"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly)})
	}
	return nil
}
