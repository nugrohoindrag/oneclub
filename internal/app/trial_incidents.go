package app

// Trial dataset: operational incidents for Management › Incidents. At the
// current date the Caddy Master and the Golf Manager record the caddy and
// golf cart incidents of the last three months (dated when they happened;
// most of the older ones closed with the action taken, two cart damages
// charged to the player through approval) and the Banquet Manager notes
// the incidents of the latest events.

import (
	"context"
	"fmt"
	"time"

	"oneclub/internal/kernel/clock"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "incidents", Order: 88, Final: trialIncidentsFinal})
}

var trialCaddyIncidents = []struct{ Category, Severity, Description, Action string }{
	{"late", "low", "Arrived 20 minutes after the call time; flight waited at the first tee", "Verbal warning, noted in the caddy file"},
	{"late", "low", "Late for the morning briefing", "Verbal warning"},
	{"misconduct", "medium", "Used the mobile phone during the round; guest complained", "Written warning SP-1"},
	{"misconduct", "high", "Argued with a member about the yardage on hole 7", "Suspended two days, apology to the member"},
	{"lost_item", "low", "Player's rangefinder left in the bag room", "Item returned to the player"},
	{"lost_item", "medium", "Head cover of a driver missing after the round", "Replaced by the club, caddy briefed"},
	{"uniform", "low", "Not wearing the club cap and towel", "Reminder at the briefing"},
	{"safety", "high", "Walked in front of a player hitting from the fairway", "Safety refresher training"},
	{"misconduct", "medium", "Asked the guest for an extra tip", "Written warning SP-1"},
	{"late", "low", "Left the flight at the halfway house without notice", "Verbal warning"},
}

var trialCartIncidents = []struct{ Category, Severity, Description, Action, Damage string }{
	{"damage", "medium", "Front bumper cracked against the bridge rail on hole 12", "Bumper replaced", "750000"},
	{"accident", "high", "Cart slid down the slope near the 15th green in the rain; no injury", "Area roped off when wet, cart inspected", "1500000"},
	{"breakdown", "low", "Battery flat at hole 9; replacement cart sent", "Battery replaced", ""},
	{"breakdown", "medium", "Brake not holding on the downhill path to hole 4", "Brake adjusted, cart out of service one day", ""},
	{"damage", "low", "Windshield scratched by a golf ball", "Windshield polished", ""},
	{"misuse", "medium", "Cart driven onto the green apron", "Player reminded of the cart rules", ""},
	{"breakdown", "low", "GPS screen not starting", "Screen reset by the technician", ""},
}

var trialBanquetIncidents = []struct{ Severity, Note string }{
	{"medium", "Sound system dropped out for 5 minutes during the speeches; backup mixer used"},
	{"low", "Late delivery of the dessert station, 15 minutes behind the run sheet"},
	{"high", "Guest slipped near the buffet; first aid given, floor dried and signage added"},
	{"low", "Air conditioning too cold in the ballroom, set point raised"},
}

func trialIncidentsFinal(_ context.Context, t *Trial) error {
	r := t.Rand("incidents")
	cm, gm, bm := t.As(trialCaddyMaster), t.As(trialGolfManager), t.As(trialBanquetManager)
	now := clock.Now().In(t.Loc)
	// Spread over the last 90 days, a few in the last week so the running month has some.
	at := func(i, n int) (time.Time, int) {
		days := 2 + (88*i)/n + r.IntN(3)
		if i >= n-3 {
			days = r.IntN(7)
		}
		d := time.Date(now.Year(), now.Month(), now.Day(), 7+r.IntN(9), r.IntN(60), 0, 0, t.Loc).AddDate(0, 0, -days)
		if d.After(now) { // later today: it happened yesterday
			d, days = d.AddDate(0, 0, -1), days+1
		}
		return d, days
	}

	var caddies, carts []J
	for _, c := range cm.Items("/api/v1/golf/caddies?limit=200") {
		if c.S("status") == "active" {
			caddies = append(caddies, c)
		}
	}
	for _, c := range gm.Items("/api/v1/golf/golf-carts?limit=200") {
		if c.S("status") == "active" {
			carts = append(carts, c)
		}
	}

	n := 16
	for i := 0; i < n && len(caddies) > 0; i++ {
		inc := trialCaddyIncidents[r.IntN(len(trialCaddyIncidents))]
		when, ago := at(i, n)
		c := caddies[r.IntN(len(caddies))]
		res := cm.Post("/api/v1/golf/caddy-incidents", J{"caddyId": c.S("id"), "category": inc.Category, "severity": inc.Severity,
			"description": inc.Description, "occurredAt": when.Format(time.RFC3339)})
		if ago > 5 && r.IntN(10) < 8 {
			cm.Post(fmt.Sprintf("/api/v1/golf/caddy-incidents/%s:close", res.S("id")), J{"reason": inc.Action})
		}
	}

	// Players of past rounds for the damage charges.
	var players []string
	for _, b := range gm.Items("/api/v1/golf/bookings?from=" + now.AddDate(0, 0, -60).Format(time.DateOnly) + "&to=" +
		now.AddDate(0, 0, -2).Format(time.DateOnly) + "&limit=20") {
		if len(players) == 2 {
			break
		}
		for _, p := range gm.Get("/api/v1/golf/bookings/" + b.S("id")).A("players") {
			if p.S("id") != "" {
				players = append(players, p.S("id"))
				break
			}
		}
	}
	n = 11
	for i := 0; i < n && len(carts) > 0; i++ {
		inc := trialCartIncidents[i%len(trialCartIncidents)]
		when, ago := at(i, n)
		body := J{"golfCartId": carts[r.IntN(len(carts))].S("id"), "category": inc.Category, "severity": inc.Severity,
			"description": inc.Description, "occurredAt": when.Format(time.RFC3339)}
		if inc.Damage != "" && i < len(trialCartIncidents) && len(players) > 0 {
			body["damageAmount"], body["playerId"] = inc.Damage, players[0]
			players = players[1:]
		}
		res := gm.Post("/api/v1/golf/golf-cart-incidents", body)
		if ago > 5 && r.IntN(10) < 7 {
			gm.Post(fmt.Sprintf("/api/v1/golf/golf-cart-incidents/%s:close", res.S("id")), J{"reason": inc.Action})
		}
	}

	// Incident notes of the latest events (dated today: they are written now).
	k := 0
	for _, e := range bm.Items("/api/v1/banquet/events?filter[status]=definite,completed&from=" + now.AddDate(0, 0, -30).Format(time.DateOnly) +
		"&to=" + now.AddDate(0, 0, 7).Format(time.DateOnly) + "&limit=20") {
		if k == len(trialBanquetIncidents) {
			break
		}
		inc := trialBanquetIncidents[k]
		bm.Post("/api/v1/banquet/events/"+e.S("id")+"/incidents", J{"severity": inc.Severity, "note": inc.Note})
		k++
	}
	return nil
}
