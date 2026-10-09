package app

// Trial dataset: the features of the demo feedback (9 Oct 2026) at the
// current date, so a complete dataset shows them too (a final step runs once
// on a dataset seeded before): the caddies' monthly base salary, the 1–5
// ratings the players gave their caddy at the front desk after the round,
// driving range bookings for today and the coming week, and the six tee
// houses with today's orders in every service status.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/clock"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "demo-feedback", Order: 89, Final: trialDemoFeedbackFinal})
}

var trialRatingComments = []string{"Reads the greens well", "Friendly and on time", "Knows every hole", "Helpful with club selection", ""}

func trialDemoFeedbackFinal(_ context.Context, t *Trial) error {
	r := t.Rand("demo-feedback")
	gm, fd := t.As(trialGolfManager), t.As(trialFrontDesk)

	// Monthly base salary (gaji pokok) of every caddy.
	for _, c := range gm.Items("/api/v1/golf/caddies?limit=500") {
		gm.Put("/api/v1/golf/caddies/"+c.S("id")+"/profile", J{"baseSalary": fmt.Sprint(2500000 + 250000*r.IntN(5))})
	}

	// Ratings at check-out for most completed rounds of the last 30 days.
	for d := t.Today.AddDate(0, 0, -30); d.Before(t.Today); d = d.AddDate(0, 0, 1) {
		for _, a := range gm.Items("/api/v1/golf/caddy-assignments?date=" + d.Format(time.DateOnly)) {
			players, _ := a["playerIds"].([]any)
			if a.S("status") != "completed" || len(players) == 0 || r.IntN(10) < 3 {
				continue
			}
			rating := []int{5, 5, 5, 4, 4, 4, 3}[r.IntN(7)]
			fd.Try("POST", "/api/v1/golf/caddy-assignments/"+a.S("id")+":rate", J{"playerId": jstr(players[0]), "rating": rating,
				"comment": trialRatingComments[r.IntN(len(trialRatingComments))]})
		}
	}

	// Driving range: today (from the next half hour) and the coming six days,
	// members and walk-in guests, a bay and a time or only the visit.
	now := clock.Now().In(t.Loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.Loc)
	members := t.Members()
	guests := []string{"Andi Saputra", "Budi Hartono", "Citra Lestari", "Dimas Pratama", "Eka Wulandari", "Fajar Nugroho", "Gita Maharani", "Hadi Kusuma"}
	for i := range 7 {
		day := today.AddDate(0, 0, i)
		n := 5 + r.IntN(4)
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			n += 4
		}
		for range n {
			minutes := []int{60, 60, 90, 120}[r.IntN(4)] // the bay time ends by closing
			at := day.Add(time.Duration(2*experience.RangeOpenHour+r.IntN(2*(experience.RangeCloseHour-experience.RangeOpenHour)-minutes/30+1)) * 30 * time.Minute)
			if !at.After(now.Add(30 * time.Minute)) {
				continue
			}
			body := J{"date": day.Format(time.DateOnly), "time": at.Format("15:04"), "minutes": minutes,
				"area": []string{"outdoor", "outdoor", "indoor"}[r.IntN(3)], "reserveBay": r.IntN(3) > 0, "players": 1 + r.IntN(2)}
			if r.IntN(2) == 0 && len(members) > 0 {
				body["customerId"] = members[r.IntN(len(members))].CustomerID
			} else {
				body["guestName"], body["guestPhone"] = guests[r.IntN(len(guests))], fmt.Sprintf("+62812%07d", r.IntN(10000000))
			}
			fd.Post("/api/v1/golf/range-bookings", body)
		}
	}

	// Tee houses (a dataset seeded before they existed gets them now) and
	// today's orders: paid or still open, each in a service status.
	if len(t.Admin().Items("/api/v1/commercial/tee-houses")) == 0 {
		t.Admin().Post("/api/v1/commercial/tee-houses:setup", J{})
	}
	outlet := t.ids("tee-house", "/api/v1/commercial/outlets?limit=100")
	product := t.ids("product", "/api/v1/commercial/products?limit=200")
	menu := []string{"AIR-600", "ISOTONIK", "COLA", "SANDWICH", "FRIES", "KOPI", "TEH", "MIEGOR"}
	pos := t.As("pos.halfway@demo.oneclub.id")
	for i := 1; i <= 6; i++ {
		code := fmt.Sprintf("TH%d", i)
		if outlet(code) == "" {
			continue
		}
		shift := pos.Post("/api/v1/commercial/shifts:open", J{"outletId": outlet(code), "openingCash": "300000"}).S("id")
		for range 3 + r.IntN(3) {
			var lines []J
			for range 1 + r.IntN(3) {
				lines = append(lines, J{"productId": product(menu[r.IntN(len(menu))]), "quantity": fmt.Sprint(1 + r.IntN(2))})
			}
			ord := pos.Post("/api/v1/commercial/orders", J{"outletId": outlet(code), "shiftId": shift, "lines": lines, "send": true,
				"tableNo": fmt.Sprintf("Hole %d", 1+r.IntN(18)), "guestCount": 1 + r.IntN(4)})
			if r.IntN(3) > 0 {
				tender := []J{{"methodType": "qris", "reference": fmt.Sprintf("QR-%08d", r.IntN(100000000))}, {"methodType": "cash"},
					{"methodType": "card", "reference": fmt.Sprintf("EDC-%06d", r.IntN(1000000))}}[r.IntN(3)]
				pos.Post("/api/v1/commercial/orders/"+ord.S("id")+":pay", J{"shiftId": shift, "tenders": []J{tender}})
			}
		}
	}
	kds := t.As(trialOutletManager)
	for _, k := range kds.Items("/api/v1/commercial/kitchen-orders") {
		if !strings.HasPrefix(k.S("outletName"), "Tee House") {
			continue
		}
		for _, state := range []string{"preparing", "ready", "served"}[:r.IntN(4)] {
			kds.Post("/api/v1/commercial/kitchen-orders/"+k.S("id")+":state", J{"state": state})
		}
	}
	return nil
}
