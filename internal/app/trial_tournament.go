package app

// Trial dataset: golf tournaments. Two club tournaments on the demo course:
// a Club Championship played two weeks ago (registrations over the three
// weeks before, draw the day before, shotgun start, every card scored,
// attested and validated, special prizes awarded, results finalized) and
// the Charity Cup in two weeks (registration open, the field filling up).
// Registration closes the course on the tee sheet for the morning of the
// tournament, so the daily golf traffic of that day plays the afternoon.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "tournaments", Order: 25, Setup: trialTournamentSetup, Day: trialTournamentDay})
}

const trialGolfAdmin = "golf.admin@demo.oneclub.id"

// trialTournament is one tournament of the dataset.
type trialTournament struct {
	Code, Name, Type string
	Day              time.Time // play date
	Field, Players   int       // field size, registrations planned
}

// trialTournamentPlan places the tournaments relative to the history: the
// last Saturday at least 14 days ago (or the latest Saturday of a short
// history) and the first Saturday at least 10 days ahead (the demo Monthly
// Medal stays a draft without a tee sheet block).
func trialTournamentPlan(t *Trial) []trialTournament {
	var out []trialTournament
	past := t.Today.AddDate(0, 0, -14)
	if past.Before(t.Start.AddDate(0, 0, 1)) {
		past = t.Today.AddDate(0, 0, -1)
	}
	for past.Weekday() != time.Saturday {
		past = past.AddDate(0, 0, -1)
	}
	if !past.Before(t.Start.AddDate(0, 0, 1)) {
		out = append(out, trialTournament{Code: "TRL-CHAMP", Name: "Club Championship " + fmt.Sprint(past.Year()), Type: "club_championship", Day: past,
			Field: 48, Players: 40})
	}
	next := t.Today.AddDate(0, 0, 10)
	for next.Weekday() != time.Saturday {
		next = next.AddDate(0, 0, 1)
	}
	if next.Before(t.Today.AddDate(0, 0, t.Ahead)) {
		out = append(out, trialTournament{Code: "TRL-CHARITY", Name: "Charity Cup " + fmt.Sprint(next.Year()), Type: "charity", Day: next, Field: 40, Players: 24})
	}
	return out
}

// regDay is the day player i registers: spread over the three weeks before
// the tournament until two days before.
func (tr trialTournament) regDay(i int) time.Time {
	return tr.Day.AddDate(0, 0, -21+i*19/tr.Players)
}

func trialTournamentSetup(_ context.Context, t *Trial) error {
	ga, gm := t.As(trialGolfAdmin), t.As(trialGolfManager)
	for _, tr := range trialTournamentPlan(t) {
		x := ga.Post("/api/v1/golf/tournaments", J{"code": tr.Code, "name": tr.Name, "tournamentType": tr.Type, "courseId": t.course(), "format": "stableford",
			"scoringBasis": "gross_and_net", "fieldSize": tr.Field, "startType": "shotgun", "public": true, "leaderboardPublic": true,
			"eligibility": "members_and_guests", "rounds": []J{{"playDate": tr.Day.Format(time.DateOnly), "startTime": "07:00"}}})
		id := x.S("id")
		t.SetRef("tournament:"+tr.Code, id)
		base := "/api/v1/golf/tournaments/" + id
		divs := map[string]string{}
		for i, d := range [][4]string{{"A", "Flight A", "0", "12.4"}, {"B", "Flight B", "12.5", "22.4"}, {"C", "Flight C", "22.5", "54"}} {
			divs[d[0]] = ga.Post(base+"/divisions", J{"code": d[0], "name": d[1], "handicapMin": d[2], "handicapMax": d[3], "sequence": i + 1}).S("id")
		}
		ga.Post(base+"/fees", J{"component": "entry_fee", "name": "Tournament Fee", "playerType": "member", "amount": "350000"})
		ga.Post(base+"/fees", J{"component": "entry_fee", "name": "Tournament Fee", "playerType": "guest", "amount": "750000"})
		gm.Post(base+"/prizes", J{"category": "gross", "position": 1, "name": "Best Gross", "value": "3000000"})
		for _, d := range []string{"A", "B", "C"} {
			for pos := 1; pos <= 2; pos++ {
				gm.Post(base+"/prizes", J{"category": "stableford", "divisionId": divs[d], "position": pos, "name": fmt.Sprintf("Flight %s #%d", d, pos),
					"value": fmt.Sprint(2000000 / pos)})
			}
		}
		gm.Post(base+"/prizes", J{"category": "nearest_to_pin", "holeNumber": 7, "name": "Nearest to Pin #7", "value": "1000000"})
		gm.Post(base+"/prizes", J{"category": "longest_drive", "holeNumber": 8, "name": "Longest Drive #8", "value": "1000000"})
		ga.Post(base+":open-registration", J{})
		for i := range tr.Players {
			if tr.regDay(i).Before(t.Start) {
				trialRegister(t, tr, i)
			}
		}
	}
	return nil
}

// trialRegister registers player i (members, some with a guest), paid at
// the desk right away or on the day.
func trialRegister(t *Trial, tr trialTournament, i int) {
	ga := t.As(trialGolfAdmin)
	base := "/api/v1/golf/tournaments/" + t.Ref("tournament:"+tr.Code)
	r := t.Rand(fmt.Sprintf("trn:%s:%d", tr.Code, i))
	hcp := fmt.Sprintf("%d.%d", 3+r.IntN(30), r.IntN(10))
	body := J{"handicapIndex": hcp, "publicConsent": r.IntN(4) > 0, "shirtSize": []string{"M", "L", "XL"}[r.IntN(3)]}
	ms := t.MembersOn(tr.Day)
	if i%4 != 3 && len(ms) > 0 {
		// members in a fixed order, each once
		m := ms[(i*7)%len(ms)]
		body["customerId"] = m.CustomerID.String()
	} else {
		g := []string{"M", "F"}[r.IntN(4)/3]
		p := trialPersonOf(r, 50000+i*13+len(tr.Code), g)
		body["guest"] = J{"name": p.Name, "phone": p.Phone, "gender": map[string]string{"M": "male", "F": "female"}[g]}
	}
	st, reg, raw := ga.Call("POST", base+"/registrations", body)
	if st == 409 { // already registered (the member list of a short history repeats)
		return
	}
	if st != 201 {
		t.fail("POST %s/registrations: %d %s", base, st, raw)
	}
	if r.IntN(5) > 0 {
		trialPayRegistration(t, base, reg.S("id"), r)
	}
}

func trialPayRegistration(t *Trial, base, id string, r *rand.Rand) {
	method := []string{"card", "cash", "qris", "bank_transfer"}[r.IntN(4)]
	t.As(trialGolfAdmin).Post(base+"/registrations/"+id+":pay", J{"methodType": method, "reference": fmt.Sprintf("TRN-%06d", r.IntN(1000000))})
}

func trialTournamentDay(_ context.Context, t *Trial, day time.Time) error {
	for _, tr := range trialTournamentPlan(t) {
		base := "/api/v1/golf/tournaments/" + t.Ref("tournament:"+tr.Code)
		if t.Ref("tournament:"+tr.Code) == "" {
			x := t.As(trialGolfAdmin).Items("/api/v1/golf/tournaments?limit=50")
			for _, v := range x {
				t.SetRef("tournament:"+v.S("code"), v.S("id"))
			}
			base = "/api/v1/golf/tournaments/" + t.Ref("tournament:"+tr.Code)
		}
		ds := day.Format(time.DateOnly)
		t.At(day, "10:30")
		for i := range tr.Players {
			if tr.regDay(i).Format(time.DateOnly) == ds {
				trialRegister(t, tr, i)
			}
		}
		switch ds {
		case tr.Day.AddDate(0, 0, -1).Format(time.DateOnly):
			t.At(day, "16:00")
			ga := t.As(trialGolfAdmin)
			ga.Post(base+":close-registration", J{"reason": "Field complete"})
			ga.Post(base+"/flights:generate", J{"method": "handicap"})
			ga.Post(base+":publish-draw", J{})
		case tr.Day.Format(time.DateOnly):
			trialPlayTournament(t, tr, base)
		}
	}
	return nil
}

// trialPlayTournament: check-in (the unpaid pay at the desk), shotgun
// start, scoring desk, special prizes and the final results.
func trialPlayTournament(t *Trial, tr trialTournament, base string) {
	ga, gm, desk := t.As(trialGolfAdmin), t.As(trialGolfManager), t.As(trialStarter)
	r := t.Rand("trn-day:" + tr.Code)
	t.At(tr.Day, "06:00")
	regs := ga.Items(base + "/registrations?limit=200&filter[status]=registered")
	for _, rg := range regs {
		if rg.S("paymentStatus") != "paid" && rg.S("paymentStatus") != "waived" {
			trialPayRegistration(t, base, rg.S("id"), r)
		}
	}
	t.Parallel(len(regs), trialWorkers, func(i int) {
		desk.Post(base+"/registrations/"+regs[i].S("id")+":check-in", nil)
	})
	t.At(tr.Day, "07:00")
	desk.Post(base+":start", nil)
	// the scoring desk enters the paper cards after the round
	t.At(tr.Day, "12:45")
	scores := ga.Items(base + "/scores?limit=200")
	t.Parallel(len(scores), trialWorkers, func(i int) {
		s := scores[i]
		rr := t.Rand("trn-card:" + s.S("registrationId"))
		hcp := 6 + rr.IntN(22)
		var entries []J
		for h := 1; h <= 18; h++ {
			entries = append(entries, J{"seq": h, "strokes": demoHoles[h-1][0] + trialOverPar(rr, hcp), "putts": 1 + rr.IntN(3)})
		}
		ga.Post(base+"/scores", J{"registrationId": s.S("registrationId"), "entries": entries})
		ga.Post(base+"/scores/"+s.S("scoreId")+":attest", J{"attestedBy": "Marker"})
		ga.Post(base+"/scores/"+s.S("scoreId")+":validate", nil)
	})
	t.At(tr.Day, "13:30")
	for _, p := range gm.Get(base).A("prizes") {
		if c := p.S("category"); c == "nearest_to_pin" || c == "longest_drive" {
			w := scores[r.IntN(len(scores))].S("registrationId")
			res := fmt.Sprintf("%d.%02d m", 1+r.IntN(4), r.IntN(100))
			if c == "longest_drive" {
				res = fmt.Sprintf("%d m", 240+r.IntN(60))
			}
			gm.Post(base+"/prizes/"+p.S("id")+":award", J{"registrationId": w, "resultText": res})
		}
	}
	gm.Post(base+":finalize", nil)
}
