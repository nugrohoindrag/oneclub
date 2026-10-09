package app

// Trial dataset: golf. Every simulated day the tee sheet is generated, the
// caddies clock in, members and visitors book tee times (back office and
// walk-in), pay at the cashier or charge the member account, get caddies
// and golf carts, check in, tee off from the starter queue and finish;
// some flights record their scores on the digital scorecard (handicaps,
// statistics), a few bookings are cancelled or end as no-shows. The
// driving range sells buckets every day.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "golf", Order: 20, Setup: trialGolfSetup, Day: trialGolfDay, Final: trialGolfFinal})
}

const (
	trialGolfManager = "golf.manager@demo.oneclub.id"
	trialStarter     = "starter@demo.oneclub.id"
	trialFrontDesk   = "front.desk@demo.oneclub.id"
	trialCaddyMaster = "caddy.master@demo.oneclub.id"
)

// course returns the demo championship course id.
func (t *Trial) course() string {
	if v := t.Ref("course"); v != "" {
		return v
	}
	for _, c := range t.As(trialGolfManager).Items("/api/v1/golf/courses?limit=50") {
		if c.S("code") == DemoCourseCode {
			t.SetRef("course", c.S("id"))
		}
	}
	return t.Ref("course")
}

func trialGolfSetup(_ context.Context, t *Trial) error {
	gm := t.As(trialGolfManager)
	r := t.Rand("caddies")
	for i := 21; i <= 80; i++ { // the caddy pool of a busy course (the demo has 20)
		g := "female"
		if i%5 == 0 {
			g = "male"
		}
		p := trialPersonOf(r, 20000+i, map[string]string{"female": "F", "male": "M"}[g])
		gm.Post("/api/v1/golf/caddies", J{"code": fmt.Sprintf("C%03d", i), "name": p.Name, "gender": g, "phone": p.Phone})
	}
	for _, c := range [][3]string{{"PREOP", "Pre-operation check", "pre_op"}, {"POSTOP", "Post-operation check", "post_op"}} {
		gm.Post("/api/v1/golf/golf-cart-checklists", J{"code": c[0], "name": c[1], "inspectionKind": c[2], "items": trialCartChecks})
	}
	for i := 31; i <= 60; i++ { // the golf cart fleet (the demo has 30)
		gm.Post("/api/v1/golf/golf-carts", J{"code": fmt.Sprintf("B%02d", i), "name": fmt.Sprintf("Buggy %02d", i)})
	}
	// MGCC's driving range is outdoor only: no indoor bays, and the front
	// desk keeps the Indoor area switched off
	for i := 1; i <= 12; i++ {
		gm.Post("/api/v1/golf/range-bays", J{"code": fmt.Sprintf("RB%02d", i), "name": fmt.Sprintf("Range Bay %02d", i), "area": "outdoor"})
	}
	gm.Put("/api/v1/golf/range-areas/indoor", J{"enabled": false})
	return nil
}

// trialSlots generates the tee sheet of a day and returns the free slots
// of the morning and afternoon sessions in start order.
func trialSlots(t *Trial, day time.Time) []J {
	gm := t.As(trialGolfManager)
	course := t.course()
	ds := day.Format(time.DateOnly)
	gm.Post("/api/v1/golf/tee-sheets:generate", J{"courseId": course, "from": ds, "days": 1})
	var out []J
	for _, s := range gm.Items("/api/v1/golf/tee-times?date=" + ds + "&courseId=" + course + "&limit=500") {
		if (s.S("session") == "morning" || s.S("session") == "afternoon") && s.S("status") == "available" && s.N("used") == 0 {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].S("startAt") != out[j].S("startAt") {
			return out[i].S("startAt") < out[j].S("startAt")
		}
		return out[i].N("startTee") < out[j].N("startTee")
	})
	return out
}

// trialBookingBody builds a booking: members with their guests, or
// visitors (some of them known customers).
func trialBookingBody(t *Trial, r *rand.Rand, day time.Time, slot J, walkIn bool) (J, bool) {
	members, guests := t.MembersOn(day), t.Guests()
	n := 2 + r.IntN(3)
	if r.IntN(5) < 3 && len(members) > 0 { // member booking
		host := members[r.IntN(len(members))]
		players := []J{{"playerType": "member", "memberNo": host.No}}
		for len(players) < n {
			if r.IntN(2) == 0 {
				m := members[r.IntN(len(members))]
				dup := false
				for _, p := range players {
					if p["memberNo"] == m.No {
						dup = true
					}
				}
				if !dup {
					players = append(players, J{"playerType": "member", "memberNo": m.No})
					continue
				}
			}
			g := trialPersonOf(r, 7000+r.IntN(2000), "M")
			players = append(players, J{"playerType": "guest_of_member", "name": g.Name, "phone": g.Phone, "hostIndex": 0})
		}
		body := J{"bookingType": "member", "channel": "back_office", "teeTimeId": slot.S("id"), "players": players}
		charge := r.IntN(3) > 0
		if charge {
			body["paymentMode"] = "member_charge"
		}
		return body, charge
	}
	var players []J
	contact, phone := "", ""
	for range n {
		if r.IntN(3) > 0 && len(guests) > 0 {
			g := guests[r.IntN(len(guests))]
			players = append(players, J{"playerType": "non_member", "customerId": g.ID, "name": g.Name})
			if contact == "" {
				contact, phone = g.Name, g.Phone
			}
			continue
		}
		g := trialPersonOf(r, 9000+r.IntN(3000), "M")
		players = append(players, J{"playerType": "non_member", "name": g.Name, "phone": g.Phone})
		if contact == "" {
			contact, phone = g.Name, g.Phone
		}
	}
	ch := "back_office"
	if walkIn {
		ch = "walk_in"
	}
	return J{"bookingType": "non_member", "channel": ch, "teeTimeId": slot.S("id"), "contactName": contact, "contactPhone": phone, "players": players}, false
}

var trialCartChecks = []string{"Brakes", "Battery", "Tyres", "Lights", "Bag rack"}

// trialWorkers is the number of parallel API workers of the seeders.
const trialWorkers = 4

// trialGolfPlan is one booking of a simulated day (choices drawn first).
type trialGolfPlan struct {
	body       J
	fate       int // 0 cancelled, 1 no-show, else played
	method     string
	score      bool
	holes, tip int
	bk         J
	flight     string
}

func trialGolfDay(ctx context.Context, t *Trial, day time.Time) error {
	r := t.Rand("golf:" + day.Format(time.DateOnly))
	ds := day.Format(time.DateOnly)
	gm, cashier, desk, starter, cm := t.As(trialGolfManager), t.As(trialCashier), t.As(trialFrontDesk), t.As(trialStarter), t.As(trialCaddyMaster)
	t.At(day, "05:00")
	slots := trialSlots(t, day)
	// caddies clock in for the day
	var entries []J
	for _, c := range cm.Items("/api/v1/golf/caddies?limit=200") {
		if c.S("status") == "active" && r.IntN(10) > 0 {
			entries = append(entries, J{"caddyId": c.S("id"), "status": "present"})
		}
	}
	cm.Put("/api/v1/golf/caddy-availability", J{"date": ds, "entries": entries})
	// golf carts back from yesterday's rounds: post-operation check, then
	// the pre-operation check of the morning makes them Ready
	var results []J
	for _, it := range trialCartChecks {
		results = append(results, J{"item": it, "pass": true})
	}
	var carts []J
	for _, c := range gm.Items("/api/v1/golf/golf-cart-board?date=" + ds) {
		if c.S("readiness") == "charging" || c.S("readiness") == "not_ready" {
			carts = append(carts, J{"id": c.S("id"), "hours": fmt.Sprint(100 + r.IntN(900)), "battery": 95 + r.IntN(6)})
		}
	}
	t.Parallel(len(carts), trialWorkers, func(i int) {
		c := carts[i]
		gm.Post("/api/v1/golf/golf-cart-inspections", J{"golfCartId": c.S("id"), "kind": "post_op", "results": results, "hourMeter": c.S("hours")})
		gm.Post("/api/v1/golf/golf-cart-inspections", J{"golfCartId": c.S("id"), "kind": "pre_op", "results": results, "batteryPercent": c["battery"]})
	})

	weekend := day.Weekday() == time.Saturday || day.Weekday() == time.Sunday
	n := 5 + r.IntN(4)
	if weekend {
		n = 10 + r.IntN(4)
	}
	// spread the flights over the free slots of the day; every choice is
	// drawn first, then the bookings are made in parallel
	var plans []*trialGolfPlan
	for i := 0; i < len(slots) && len(plans) < n; i++ {
		if r.IntN(3) == 0 || len(slots)-i <= n-len(plans) {
			body, _ := trialBookingBody(t, r, day, slots[i], len(plans)%4 == 3)
			p := &trialGolfPlan{body: body, fate: r.IntN(40), method: []string{"cash", "card", "card", "qris", "bank_transfer"}[r.IntN(5)],
				score: r.IntN(4) == 0, holes: 18, tip: -1}
			if r.IntN(25) == 0 {
				p.holes = 9
			}
			if r.IntN(3) == 0 {
				p.tip = 50000 + 25000*r.IntN(3)
			}
			plans = append(plans, p)
		}
	}
	t.Parallel(len(plans), trialWorkers, func(i int) {
		p := plans[i]
		p.bk = trialBook(t, p.body)
		if p.fate <= 1 {
			return
		}
		if p.bk.S("paymentMode") != "member_charge" {
			if f := p.bk.M("folio"); f.S("balance") != "" && f.S("balance") != "0" {
				trialPayFolio(t, cashier, p.bk.S("folioId"), p.method)
			}
		}
		if fl := p.bk.A("flights"); len(fl) > 0 {
			p.flight = fl[0].S("id")
		}
	})
	// two waves (morning and afternoon): caddies and golf carts (assigned
	// one by one from the queues), check-in, tee-off from the starter queue,
	// scores, finish
	waves := [2][]*trialGolfPlan{}
	for _, p := range plans {
		if p.flight == "" {
			continue
		}
		w := 0
		if p.bk.S("localTime") >= "11:00" {
			w = 1
		}
		waves[w] = append(waves[w], p)
	}
	for w, wave := range waves {
		t.At(day, []string{"05:45", "11:15"}[w])
		if w == 0 {
			for _, p := range plans {
				if p.fate == 0 {
					gm.Post("/api/v1/golf/bookings/"+p.bk.S("id")+":cancel", J{"reason": "Guest cancelled (business trip)"})
				}
			}
		}
		for _, p := range wave {
			cm.Post("/api/v1/golf/caddy-assignments", J{"flightId": p.flight, "auto": true})
			gm.Post("/api/v1/golf/golf-cart-assignments", J{"flightId": p.flight, "auto": true})
		}
		t.Parallel(len(wave), trialWorkers, func(i int) {
			desk.Post("/api/v1/golf/check-ins", J{"method": "booking_code", "value": wave[i].bk.S("code"), "date": ds})
		})
		t.At(day, []string{"06:30", "12:00"}[w])
		t.Parallel(len(wave), trialWorkers, func(i int) {
			starter.Post("/api/v1/golf/starter-queue/"+wave[i].flight+":tee-off", J{})
		})
		t.At(day, []string{"06:35", "12:05"}[w]) // dispatch: rounds started, scorecards opened
		t.Parallel(len(wave), trialWorkers, func(i int) {
			if wave[i].score {
				trialScores(t, t.Rand("scores:"+wave[i].flight), wave[i].flight)
			}
		})
		t.At(day, []string{"11:00", "16:30"}[w])
		t.Parallel(len(wave), trialWorkers, func(i int) {
			p := wave[i]
			starter.Post("/api/v1/golf/starter-queue/"+p.flight+":finish", J{"holesPlayed": p.holes})
			if p.tip > 0 { // tip for the caddy
				for _, ca := range gm.Items("/api/v1/golf/caddy-assignments?flightId=" + p.flight) {
					gm.Post("/api/v1/golf/caddy-tips", J{"assignmentId": ca.S("id"), "amount": fmt.Sprint(p.tip), "method": "cash"})
					break
				}
			}
		})
		if w == 1 {
			for _, p := range plans {
				if p.fate == 1 {
					gm.Post("/api/v1/golf/bookings/"+p.bk.S("id")+":no-show", J{"reason": "Did not show up at the tee time"})
				}
			}
		}
	}
	trialRangeDay(t, r, day)
	return nil
}

// trialScores records the scores of the players of a flight on their
// digital scorecards, validates and finalizes them.
func trialScores(t *Trial, r *rand.Rand, flight string) {
	gm := t.As(trialGolfManager)
	rd := gm.Get("/api/v1/golf/rounds/" + flight).M("round")
	for _, p := range rd.A("players") {
		sc := p.S("scorecardId")
		if sc == "" {
			continue
		}
		hcp := 8 + r.IntN(20)
		var entries []J
		for s := 1; s <= 18; s++ {
			entries = append(entries, J{"seq": s, "strokes": demoHoles[s-1][0] + trialOverPar(r, hcp), "putts": 1 + r.IntN(3)})
		}
		gm.Post("/api/v1/golf/scorecards/"+sc+"/scores", J{"source": "caddy", "entries": entries})
		gm.Post("/api/v1/golf/scorecards/"+sc+":validate", J{"attestedBy": "Marker"})
		gm.Post("/api/v1/golf/scorecards/"+sc+":finalize", nil)
	}
}

// trialBook creates a booking; a member charge over the member's limit is
// paid at the venue instead.
func trialBook(t *Trial, body J) J {
	gm := t.As(trialGolfManager)
	st, bk, raw := gm.Call("POST", "/api/v1/golf/bookings", body)
	if st == 409 && strings.Contains(string(raw), "credit_limit_exceeded") {
		delete(body, "paymentMode")
		return gm.Post("/api/v1/golf/bookings", body)
	}
	if st != 201 {
		t.fail("POST /api/v1/golf/bookings: %d %s", st, raw)
	}
	return bk
}

// trialOverPar is the strokes over par of one hole for a handicap.
func trialOverPar(r *rand.Rand, hcp int) int {
	x := r.IntN(36)
	switch {
	case x < 2:
		return -1
	case x < hcp/2+8:
		return 0
	case x < hcp+20:
		return 1
	case x < 33:
		return 2
	}
	return 3
}

// trialRangeDay: driving range buckets and bay sessions.
func trialRangeDay(t *Trial, r *rand.Rand, day time.Time) {
	t.At(day, "16:00")
	gm := t.As(trialGolfManager)
	guests := t.Guests()
	for range 3 + r.IntN(5) {
		g := guests[r.IntN(len(guests))]
		s := gm.Post("/api/v1/golf/range-sessions", J{"customerId": g.ID})
		gm.Post("/api/v1/golf/range-buckets", J{"customerId": g.ID, "balls": []int{50, 100, 150}[r.IntN(3)], "source": "complimentary",
			"reason": "Range promotion"})
		gm.Post("/api/v1/golf/range-sessions/"+s.S("id")+":end", nil)
	}
}

// trialGolfFinal books the upcoming tee times (next weeks).
func trialGolfFinal(_ context.Context, t *Trial) error {
	for d := 0; d < t.Ahead; d++ {
		day := t.Today.AddDate(0, 0, d)
		r := t.Rand("golf-ahead:" + day.Format(time.DateOnly))
		slots := trialSlots(t, day)
		n := 6 - d/6
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			n = 12 - d/4
		}
		if d == 0 {
			// today: only the slots that have not started yet
			var later []J
			for _, s := range slots {
				if at, err := time.Parse(time.RFC3339, s.S("startAt")); err == nil && at.After(time.Now().Add(30*time.Minute)) {
					later = append(later, s)
				}
			}
			slots = later
		}
		for i := 0; i < len(slots) && n > 0; i++ {
			if r.IntN(2) == 0 {
				continue
			}
			body, _ := trialBookingBody(t, r, day, slots[i], false)
			trialBook(t, body)
			n--
		}
	}
	return nil
}
