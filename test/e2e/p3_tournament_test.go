package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/platform/integration"
)

// ── helpers ───────────────────────────────────────────────────────────────

const trnBase = "/api/v1/golf/tournaments"

// trnDispatch runs the outbox until cond holds.
func trnDispatch(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitFor(t, 20*time.Second, what, func() bool {
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		return cond()
	})
}

// trnMemberClient returns the Member App user and its customer.
func trnMemberClient(t *testing.T, sa *Client) (*Client, string) {
	t.Helper()
	mc := roleUser(t, inst, "member")
	uid := userID(t, "member")
	var cust string
	sysQueryRow(t, inst, `SELECT coalesce((SELECT id::text FROM crm.customers WHERE user_id = $1 AND property_id = $2 LIMIT 1), '')`,
		[]any{mustUUID(uid), inst.Main}, &cust)
	if cust == "" {
		cust = customer(t, sa, fmt.Sprintf("TRNM%d", time.Now().UnixNano()%1e6), "Tara Member", map[string]any{"userId": uid, "email": "tara@member.test"})
	}
	return mc, cust
}

// trnTournamentCaddy returns the caddy linked to the Caddy Tablet user.
func trnTournamentCaddy(t *testing.T, sa *Client, sfx string) string {
	t.Helper()
	var cid string
	sysQueryRow(t, inst, `SELECT coalesce((SELECT c.id::text FROM golf.caddies c JOIN golf.caddy_profiles p ON p.caddy_id = c.id
		WHERE p.user_id = $1 AND c.property_id = $2 AND c.archived_at IS NULL LIMIT 1), '')`, []any{mustUUID(userID(t, "caddy")), inst.Main}, &cid)
	if cid != "" {
		return cid
	}
	lv := idOf(sa.Must(201, "POST", "/api/v1/golf/caddy-levels", map[string]any{"code": "TL" + sfx, "name": "Tournament level", "rank": 9, "feeAmount": "150000"}))
	cid = idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "TC" + sfx, "name": "Caddy Turnamen", "gender": "male"}))
	sa.Must(200, "PUT", "/api/v1/golf/caddies/"+cid+"/profile", map[string]any{"levelId": lv, "userId": userID(t, "caddy")})
	return cid
}

// trnEnablePayments enables the mock payment gateway and returns its webhook secret.
func trnEnablePayments(t *testing.T) string {
	t.Helper()
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": map[string]any{"autoPay": false}})
	return str(pa.Must(200, "POST", "/api/v1/platform/integrations/"+mp+":rotate-webhook-secret", nil).JSON()["webhookSecret"])
}

// trnPayWebhook settles a pending gateway payment.
func trnPayWebhook(t *testing.T, secret, externalID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": "evt_" + uuid.NewString(), "type": "payment.paid", "data": map[string]any{"externalId": externalID, "status": "paid"}})
	hook := anon(t, inst)
	hook.Property = uuid.Nil
	hook.Must(200, "POST", "/api/v1/webhooks/mock-payment", body, integration.SignatureHeader, integration.Sign(secret, body, time.Now()))
}

// trnStrokes is the strokes of a player on a hole (par + a pattern).
func trnStrokes(i, seq, par int) int {
	extra := (i + seq) % 3 // 0, 1 or 2 over par
	if extra == 2 && seq%2 == 0 {
		extra = 0
	}
	return par + extra
}

func trnEntries(pars []int, f func(seq, par int) int) []map[string]any {
	out := make([]map[string]any, 0, len(pars))
	for i, par := range pars {
		out = append(out, map[string]any{"seq": i + 1, "strokes": f(i+1, par)})
	}
	return out
}

// trnBoard returns a board of a leaderboard.
func trnBoard(t *testing.T, lb map[string]any, category, division string) []map[string]any {
	t.Helper()
	for _, b := range asMaps(lb["boards"]) {
		if b["category"] == category && b["division"] == division {
			return asMaps(b["entries"])
		}
	}
	t.Fatalf("board %s/%s missing: %v", category, division, lb["boards"])
	return nil
}

func trnPars(holes []map[string]any) []int {
	out := make([]int, len(holes))
	for i, h := range holes {
		out[i] = int(h["par"].(float64))
	}
	return out
}

// ── §9.3 Club Tournament ──────────────────────────────────────────────────

// PRD P3 §9.3 and EP-16 acceptance in one club tournament: stableford net
// with 72 players and a shotgun start — registration from the Back Office,
// the Member App and the website with fees, packages and online payment,
// waitlist promotion, fee waiver; automatic flighting by handicap (one
// flight per hole, start sheet), Tournament Desk check-in (offline queue
// replayed idempotently), scoring from the caddy tablet (offline sync), the
// scoring desk and the Member App with attestation, validation, correction
// and DQ/WD/NR; live leaderboards with countback; sponsors invoiced, special
// awards, finalize with prizes and Hall of Fame champions shown publicly
// only with consent; Tournament Report and Golf Performance KPIs.
func TestP3TournamentClub(t *testing.T) {
	sa := superAdmin(t, inst)
	ga := roleUser(t, inst, "golf_admin")
	gm := roleUser(t, inst, "golf_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	g := setupGolfCourse(t, sa, "TR"+sfx[:3])
	pars := trnPars(g.Holes)
	day := clubDay(inst, 12, isSaturday)
	secret := trnEnablePayments(t)

	// Tournament Schedule: create (FR-TRN-01) with divisions, packages, fees.
	if r := ga.Do("POST", trnBase, map[string]any{"name": "Bad", "courseId": g.Course, "format": "match_play", "fieldSize": 72,
		"rounds": []map[string]any{{"playDate": day, "startTime": "07:00"}}}); r.Status != 422 {
		t.Fatalf("format must be validated: %s", r)
	}
	tr := ga.Must(201, "POST", trnBase, map[string]any{"name": "Monthly Medal " + sfx, "courseId": g.Course, "playingRouteId": g.RouteAB,
		"format": "stableford", "scoringBasis": "gross_and_net", "fieldSize": 72, "startType": "shotgun", "public": true, "eligibility": "members_and_guests",
		"rounds": []map[string]any{{"playDate": day, "startTime": "07:00"}}}).JSON()
	tid := str(tr["id"])
	if tr["status"] != "draft" || tr["code"] == "" || len(asMaps(tr["rounds"])) != 1 || tr["policyVersion"] == nil {
		t.Fatalf("created tournament: %v", tr)
	}
	ga.Must(200, "PATCH", trnBase+"/"+tid, map[string]any{"description": "Second Saturday stableford", "leaderboardPublic": true})
	divA := idOf(ga.Must(201, "POST", trnBase+"/"+tid+"/divisions", map[string]any{"code": "A", "name": "Flight A", "handicapMin": "0", "handicapMax": "15",
		"sequence": 1, "hallOfFameDivision": "men"}))
	divB := idOf(ga.Must(201, "POST", trnBase+"/"+tid+"/divisions", map[string]any{"code": "B", "name": "Flight B", "handicapMin": "15.1", "handicapMax": "54",
		"sequence": 2}))
	tmp := idOf(ga.Must(201, "POST", trnBase+"/"+tid+"/divisions", map[string]any{"code": "TMP", "name": "Temporary"}))
	ga.Must(200, "PATCH", trnBase+"/"+tid+"/divisions/"+tmp, map[string]any{"name": "Temporary division"})
	ga.Must(204, "DELETE", trnBase+"/"+tid+"/divisions/"+tmp, nil)
	pkg := idOf(ga.Must(201, "POST", trnBase+"/"+tid+"/packages", map[string]any{"code": "PLAYER", "name": "Player Package", "isDefault": true}))
	pkg2 := idOf(ga.Must(201, "POST", trnBase+"/"+tid+"/packages", map[string]any{"code": "ENTRY", "name": "Entry only"}))
	ga.Must(200, "PATCH", trnBase+"/"+tid+"/packages/"+pkg2, map[string]any{"description": "Tournament fee without dinner"})
	tmpPkg := idOf(ga.Must(201, "POST", trnBase+"/"+tid+"/packages", map[string]any{"code": "TMP", "name": "Temporary"}))
	ga.Must(204, "DELETE", trnBase+"/"+tid+"/packages/"+tmpPkg, nil)
	ga.Must(201, "POST", trnBase+"/"+tid+"/fees", map[string]any{"component": "entry_fee", "name": "Tournament Fee", "amount": "500000"})
	dinner := idOf(ga.Must(201, "POST", trnBase+"/"+tid+"/fees", map[string]any{"packageId": pkg, "component": "dinner", "name": "Gala dinner", "amount": "150000"}))
	ga.Must(201, "POST", trnBase+"/"+tid+"/fees", map[string]any{"packageId": pkg, "component": "caddy_fee", "name": "Caddy fee", "amount": "100000"})
	ga.Must(200, "PATCH", trnBase+"/"+tid+"/fees/"+dinner, map[string]any{"amount": "200000"})
	tmpFee := idOf(ga.Must(201, "POST", trnBase+"/"+tid+"/fees", map[string]any{"component": "goodie_bag", "name": "Old fee", "amount": "1"}))
	ga.Must(204, "DELETE", trnBase+"/"+tid+"/fees/"+tmpFee, nil)
	d := ga.Must(200, "GET", trnBase+"/"+tid, nil).JSON()
	for _, p := range asMaps(d["packages"]) {
		if p["code"] == "PLAYER" && !dec(p["guestTotal"]).Equal(dec("800000")) {
			t.Fatalf("player package total: %v", p)
		}
	}

	// Sponsors (logo on the leaderboard / start sheet, billed) and prizes.
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "TRNSP" + sfx, "name": "PT Sponsor Golf " + sfx}))
	sp := idOf(gm.Must(201, "POST", trnBase+"/"+tid+"/sponsors", map[string]any{"name": "PT Sponsor Golf", "sponsorLevel": "title",
		"corporateAccountId": corp, "amount": "25000000", "holes": []int{3}, "contactEmail": "sponsor@golf.test"}))
	gm.Must(200, "PATCH", trnBase+"/"+tid+"/sponsors/"+sp, map[string]any{"packageName": "Title Sponsor"})
	tmpSp := idOf(gm.Must(201, "POST", trnBase+"/"+tid+"/sponsors", map[string]any{"name": "Withdrawn sponsor"}))
	gm.Must(204, "DELETE", trnBase+"/"+tid+"/sponsors/"+tmpSp, nil)
	prize := func(body map[string]any) string { return idOf(gm.Must(201, "POST", trnBase+"/"+tid+"/prizes", body)) }
	pGross := prize(map[string]any{"category": "gross", "position": 1, "name": "Best Gross", "value": "3000000", "sponsorId": sp})
	pStbA := prize(map[string]any{"category": "stableford", "divisionId": divA, "position": 1, "name": "Flight A Winner", "value": "2000000"})
	pStbB := prize(map[string]any{"category": "stableford", "divisionId": divB, "position": 1, "name": "Flight B Winner", "value": "2000000"})
	pNTP := prize(map[string]any{"category": "nearest_to_pin", "holeNumber": 3, "name": "Nearest to Pin #3", "value": "1000000"})
	pLD := prize(map[string]any{"category": "longest_drive", "holeNumber": 4, "name": "Longest Drive #4", "value": "1000000"})
	pHIO := prize(map[string]any{"category": "hole_in_one", "holeNumber": 3, "name": "Hole-in-One #3", "value": "50000000"})
	gm.Must(422, "POST", trnBase+"/"+tid+"/prizes", map[string]any{"category": "gross", "name": "No position"})
	gm.Must(200, "PATCH", trnBase+"/"+tid+"/prizes/"+pLD, map[string]any{"description": "Fairway 4"})
	tmpPrize := prize(map[string]any{"category": "lucky_draw", "name": "Lucky draw"})
	gm.Must(204, "DELETE", trnBase+"/"+tid+"/prizes/"+tmpPrize, nil)

	// Open registration: the course is closed on the tee sheet (FR-TRN-02).
	op := ga.Must(200, "POST", trnBase+"/"+tid+":open-registration", map[string]any{}).JSON()
	if op["status"] != "open" || asMaps(op["rounds"])[0]["courseBlockId"] == nil {
		t.Fatalf("open registration: %v", op)
	}
	var blocked int
	sysQueryRow(t, inst, `SELECT count(*) FROM golf.course_blocks WHERE course_id = $1 AND reason = 'tournament' AND status = 'active'`,
		[]any{mustUUID(g.Course)}, &blocked)
	if blocked != 1 {
		t.Fatalf("tee sheet block: %d", blocked)
	}

	// Website (FR-WEB-P3-03): public listing, registration with online payment, manage link.
	pub := anon(t, inst)
	pid := inst.Main.String()
	if ls := pub.Must(200, "GET", "/api/v1/public/tournaments?propertyId="+pid, nil).Items(); !containsID(ls, tid) {
		t.Fatalf("public tournaments: %v", ls)
	}
	pv := pub.Must(200, "GET", "/api/v1/public/tournaments/"+tid+"?propertyId="+pid, nil).JSON()
	if pv["registrationOpen"] != true || pv["placesLeft"].(float64) != 72 {
		t.Fatalf("public tournament: %v", pv)
	}
	webBody := func(name, phone string) map[string]any {
		return map[string]any{"propertyId": pid, "guest": map[string]any{"name": name, "phone": phone, "email": strings.ToLower(strings.ReplaceAll(name, " ", ".")) + "@web.test"},
			"gender": "male", "handicapIndex": "18.2", "consent": true, "paymentMethod": "qris", "packageId": pkg2}
	}
	nb := webBody("No Consent", "+62855"+sfx+"0")
	nb["consent"] = false
	pub.Must(422, "POST", "/api/v1/public/tournaments/"+tid+"/registrations", nb)
	w1 := pub.Must(201, "POST", "/api/v1/public/tournaments/"+tid+"/registrations", webBody("Web Withdrawer", "+62855"+sfx+"1")).JSON()
	if w1["status"] != "registered" || w1["manageToken"] == "" || w1["checkout"] == nil || !dec(w1["feeTotal"]).Equal(dec("500000")) {
		t.Fatalf("website registration: %v", w1)
	}
	tok := str(w1["manageToken"])
	if m := pub.Must(200, "GET", "/api/v1/public/tournament-registrations/"+tok+"?propertyId="+pid, nil).JSON(); m["canWithdraw"] != true || m["refundPercent"] != "100" {
		t.Fatalf("manage link: %v", m)
	}
	if wd := pub.Must(200, "POST", "/api/v1/public/tournament-registrations/"+tok+":withdraw?propertyId="+pid, map[string]any{}).JSON(); wd["status"] != "withdrawn" {
		t.Fatalf("website withdrawal: %v", wd)
	}
	w2 := pub.Must(201, "POST", "/api/v1/public/tournaments/"+tid+"/registrations", webBody("Web Golfer", "+62855"+sfx+"2")).JSON()
	trnPayWebhook(t, secret, str(w2["checkout"].(map[string]any)["externalId"]))
	tok2 := str(w2["manageToken"])
	trnDispatch(t, "website payment settled", func() bool {
		return pub.Must(200, "GET", "/api/v1/public/tournament-registrations/"+tok2+"?propertyId="+pid, nil).JSON()["paymentStatus"] == "paid"
	})

	// Member App (FR-APP-P3-04): register & pay by member charge or online.
	mc, memberCust := trnMemberClient(t, sa)
	if ls := mc.Must(200, "GET", "/api/v1/member/golf/tournaments", nil).Items(); !containsID(ls, tid) {
		t.Fatalf("member tournaments: %v", ls)
	}
	mr := mc.Must(201, "POST", "/api/v1/member/golf/tournaments/"+tid+"/registrations", map[string]any{"handicapIndex": "9.4", "shirtSize": "L",
		"publicConsent": true, "payment": "online", "paymentMethod": "qris"}).JSON()
	if mr["status"] != "registered" || mr["checkout"] == nil {
		t.Fatalf("member registration: %v", mr)
	}
	mc.Must(409, "POST", "/api/v1/member/golf/tournaments/"+tid+"/registrations", map[string]any{})
	trnPayWebhook(t, secret, str(mr["checkout"].(map[string]any)["externalId"]))
	var mine []map[string]any
	trnDispatch(t, "member payment settled", func() bool {
		mine = mc.Must(200, "GET", "/api/v1/member/golf/my-tournaments", nil).Items()
		for _, x := range mine {
			if x["tournamentId"] == tid && x["paymentStatus"] == "paid" {
				return true
			}
		}
		return false
	})

	// Back Office registrations (official handicap; sponsor guests paired).
	regs := map[int]string{}
	hcp := func(i int) string { return fmt.Sprintf("%d.%d", 2+(i*7)%30, i%10) }
	for i := 0; i < 70; i++ {
		body := map[string]any{"guest": map[string]any{"name": fmt.Sprintf("Pemain %02d %s", i, sfx), "phone": fmt.Sprintf("+62877%s%03d", sfx, i), "gender": "male"},
			"handicapIndex": hcp(i), "publicConsent": i%2 == 0}
		if i < 3 {
			body["pairingGroup"], body["sponsorId"] = "SPONSOR", sp
		}
		if i == 5 {
			body["packageId"] = pkg2
		}
		r := ga.Must(201, "POST", trnBase+"/"+tid+"/registrations", body, "Idempotency-Key", newKey()).JSON()
		if r["status"] != "registered" {
			t.Fatalf("registration %d: %v", i, r)
		}
		regs[i] = str(r["id"])
	}
	if r := ga.Do("POST", trnBase+"/"+tid+"/registrations", map[string]any{"guest": map[string]any{"name": fmt.Sprintf("Pemain %02d %s", 0, sfx),
		"phone": fmt.Sprintf("+62877%s%03d", sfx, 0)}}); r.Status != 409 {
		t.Fatalf("duplicate player: %s", r)
	}
	// Field full: waitlist (FR-TRN-04), automatic promotion on a withdrawal.
	wl1 := ga.Must(201, "POST", trnBase+"/"+tid+"/registrations", map[string]any{"guest": map[string]any{"name": "Waiting One " + sfx, "phone": "+62866" + sfx + "1"},
		"handicapIndex": "20"}).JSON()
	wl2 := ga.Must(201, "POST", trnBase+"/"+tid+"/registrations", map[string]any{"guest": map[string]any{"name": "Waiting Two " + sfx, "phone": "+62866" + sfx + "2"},
		"handicapIndex": "21"}).JSON()
	if wl1["status"] != "waitlisted" || wl1["waitlistPosition"].(float64) != 1 || wl2["waitlistPosition"].(float64) != 2 {
		t.Fatalf("waitlist: %v / %v", wl1, wl2)
	}
	ga.Must(422, "POST", trnBase+"/"+tid+"/registrations/"+regs[69]+":withdraw", map[string]any{})
	wd := ga.Must(200, "POST", trnBase+"/"+tid+"/registrations/"+regs[69]+":withdraw", map[string]any{"reason": "Injury"}).JSON()
	if wd["status"] != "withdrawn" || wd["promoted"] != wl1["number"] || wd["refundPercent"] != "100" {
		t.Fatalf("withdraw & promote: %v", wd)
	}
	promoted := ga.Must(200, "GET", trnBase+"/"+tid+"/registrations/"+str(wl1["id"]), nil).JSON()
	if promoted["status"] != "registered" || promoted["folioId"] == nil || promoted["paymentStatus"] != "pending" {
		t.Fatalf("promoted player: %v", promoted)
	}
	regs[69] = str(wl1["id"])
	if w := ga.Must(200, "GET", trnBase+"/"+tid+"/registrations/"+str(wl2["id"]), nil).JSON(); w["waitlistPosition"].(float64) != 1 {
		t.Fatalf("waitlist renumbered: %v", w)
	}
	// Desk payment, fee waiver for a sponsor guest, participant change.
	paid := ga.Must(200, "POST", trnBase+"/"+tid+"/registrations/"+regs[10]+":pay", map[string]any{"methodType": "cash"}, "Idempotency-Key", newKey()).JSON()
	if paid["paymentStatus"] != "paid" {
		t.Fatalf("desk payment: %v", paid)
	}
	wv := ga.Must(200, "POST", trnBase+"/"+tid+"/registrations/"+regs[0]+":waive-fee", map[string]any{"reason": "Sponsor guest"}).JSON()
	if wv["paymentStatus"] != "waived" || wv["feeWaiverStatus"] != "approved" {
		t.Fatalf("fee waiver: %v", wv)
	}
	up := ga.Must(200, "PATCH", trnBase+"/"+tid+"/registrations/"+regs[20], map[string]any{"handicapIndex": "4.0", "shirtSize": "XL"}).JSON()
	if up["handicapIndex"] != "4" || up["handicapSource"] != "official" {
		t.Fatalf("participant update: %v", up)
	}
	if ps := ga.Must(200, "GET", trnBase+"/"+tid+"/registrations?filter[status]=registered,checked_in", nil).Items(); len(ps) != 72 {
		t.Fatalf("participants: %d", len(ps))
	}
	if pv := pub.Must(200, "GET", "/api/v1/public/tournaments/"+tid+"?propertyId="+pid, nil).JSON(); pv["placesLeft"].(float64) != 0 {
		t.Fatalf("places left: %v", pv["placesLeft"])
	}
	ga.Must(200, "POST", trnBase+"/"+tid+":close-registration", map[string]any{"reason": "Field complete"})
	pub.Must(409, "POST", "/api/v1/public/tournaments/"+tid+"/registrations", webBody("Too Late", "+62855"+sfx+"9"))

	// Flighting by handicap and shotgun tee assignment (AC: 72 players,
	// 18 flights × 4, one flight per hole; the start sheet shows every start).
	dr := ga.Must(200, "POST", trnBase+"/"+tid+"/flights:generate", map[string]any{"method": "handicap"}).JSON()
	fl := asMaps(dr["flights"])
	holesUsed := map[float64]bool{}
	for _, f := range fl {
		holesUsed[f["startHole"].(float64)] = true
		if len(asMaps(f["players"])) != 4 || f["startGroup"] != nil {
			t.Fatalf("flight: %v", f)
		}
	}
	if len(fl) != 18 || len(holesUsed) != 18 {
		t.Fatalf("shotgun: %d flights on %d holes", len(fl), len(holesUsed))
	}
	// sponsor guests stay together
	together := map[string]bool{}
	for _, f := range fl {
		for _, p := range asMaps(f["players"]) {
			for i := 0; i < 3; i++ {
				if p["registrationId"] == regs[i] {
					together[str(f["id"])] = true
				}
			}
		}
	}
	if len(together) != 1 {
		t.Fatalf("sponsor guests split over flights: %v", together)
	}
	// manual flighting (drag): a full flight refuses, a new flight is made and removed again
	f0, f1 := fl[0], fl[1]
	mover := str(asMaps(f0["players"])[3]["registrationId"])
	ga.Must(409, "POST", trnBase+"/"+tid+"/flights:move-player", map[string]any{"registrationId": mover, "toFlightId": f1["id"]})
	if mv := ga.Must(200, "POST", trnBase+"/"+tid+"/flights:move-player", map[string]any{"registrationId": mover}).JSON(); len(asMaps(mv["flights"])) != 19 {
		t.Fatalf("new flight: %v", len(asMaps(mv["flights"])))
	}
	if mv := ga.Must(200, "POST", trnBase+"/"+tid+"/flights:move-player", map[string]any{"registrationId": mover, "toFlightId": f0["id"]}).JSON(); len(asMaps(mv["flights"])) != 18 {
		t.Fatalf("back to 18 flights: %v", len(asMaps(mv["flights"])))
	}
	// caddy of the tablet user on the first player of flight 1
	caddy := trnTournamentCaddy(t, sa, sfx)
	caddyPlayer := str(asMaps(f0["players"])[0]["registrationId"])
	ga.Must(200, "PATCH", trnBase+"/"+tid+"/flights/"+str(f0["id"]), map[string]any{"caddies": []map[string]any{{"registrationId": caddyPlayer, "caddyId": caddy}}})
	ss := ga.Must(200, "GET", trnBase+"/"+tid+"/start-sheet", nil).JSON()
	if ss["players"].(float64) != 72 || len(asMaps(ss["sponsors"])) != 1 {
		t.Fatalf("start sheet: players %v sponsors %v", ss["players"], ss["sponsors"])
	}
	for _, f := range asMaps(ss["flights"]) {
		if str(f["startLabel"]) == "" || f["localTime"] != "07:00" {
			t.Fatalf("start sheet line: %v", f)
		}
	}
	if pdf := ga.Must(200, "GET", trnBase+"/"+tid+"/start-sheet.pdf", nil); !strings.HasPrefix(string(pdf.Body), "%PDF") {
		t.Fatal("start sheet PDF")
	}
	mc.Must(404, "GET", "/api/v1/member/golf/tournaments/"+tid+"/start-sheet", nil) // not published yet
	ga.Must(200, "POST", trnBase+"/"+tid+":publish-draw", map[string]any{})
	if s := mc.Must(200, "GET", "/api/v1/member/golf/tournaments/"+tid+"/start-sheet", nil).JSON(); s["players"].(float64) != 72 {
		t.Fatalf("member start sheet: %v", s["players"])
	}
	for _, x := range mc.Must(200, "GET", "/api/v1/member/golf/my-tournaments", nil).Items() {
		if x["tournamentId"] == tid && x["start"] == nil {
			t.Fatalf("my start: %v", x)
		}
	}

	// Tournament Desk check-in, direct and from the offline queue (FR-OPS-P3-05).
	desk := roleUser(t, inst, "starter_marshal")
	desk.Must(200, "POST", trnBase+"/"+tid+"/registrations/"+regs[1]+":check-in", nil)
	item := map[string]any{"id": newKey(), "action": "golf.tournament_check_in", "payload": map[string]any{"tournamentId": tid, "registrationId": regs[2]}}
	for i := 0; i < 2; i++ {
		res := asMaps(desk.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{item}}).JSON()["results"])
		if (i == 0 && res[0]["status"] != "accepted") || (i == 1 && res[0]["status"] != "duplicate") {
			t.Fatalf("offline check-in replay %d: %v", i, res)
		}
	}
	if r := ga.Must(200, "GET", trnBase+"/"+tid+"/registrations/"+regs[2], nil).JSON(); r["status"] != "checked_in" {
		t.Fatalf("checked in offline: %v", r)
	}

	// Shotgun Start (Starter): every flight tees off at once.
	ga.Must(409, "POST", trnBase+"/"+tid+"/scores", map[string]any{"registrationId": regs[1], "entries": trnEntries(pars[:1], func(int, int) int { return 4 })})
	st := desk.Must(200, "POST", trnBase+"/"+tid+":start", nil).JSON()
	if st["status"] != "in_progress" {
		t.Fatalf("start: %v", st)
	}
	dr = ga.Must(200, "GET", trnBase+"/"+tid+"/flights", nil).JSON()
	for _, f := range asMaps(dr["flights"]) {
		if f["status"] != "in_play" {
			t.Fatalf("shotgun flights in play: %v", f)
		}
	}
	desk.Must(409, "POST", trnBase+"/"+tid+"/flights/"+str(f0["id"])+":tee-off", nil)
	ga.Must(409, "POST", trnBase+"/"+tid+"/registrations/"+regs[30]+":withdraw", map[string]any{"reason": "Too late"})

	// Caddy tablet: today's tournament flight in tournament format, scores
	// online and offline (synced by device time), not for other flights.
	sysExec(t, inst, `UPDATE golf.tournament_rounds SET play_date = $2 WHERE tournament_id = $1`, mustUUID(tid), clubDay(inst, 0, func(time.Weekday) bool { return true }))
	cad := roleUser(t, inst, "caddy")
	cf := cad.Must(200, "GET", "/api/v1/golf/my-tournament-flights", nil).Items()
	if len(cf) != 1 || cf[0]["format"] != "stableford" || len(asMaps(cf[0]["scorecards"])) != 4 {
		t.Fatalf("caddy tournament flights: %v", cf)
	}
	cad.Must(200, "GET", trnBase+"/"+tid+"/flights/"+str(f0["id"])+"/scorecards", nil)
	cad.Must(403, "GET", trnBase+"/"+tid+"/flights/"+str(f1["id"])+"/scorecards", nil)
	other := str(asMaps(f1["players"])[0]["registrationId"])
	cad.Must(403, "POST", trnBase+"/"+tid+"/scores", map[string]any{"registrationId": other, "entries": trnEntries(pars[:1], func(int, int) int { return 4 })})

	// strokes per player index; special cards: countback pair, Hole-in-One, DQ/WD/NR
	idx := map[string]int{}
	for i, rid := range regs {
		idx[rid] = i
	}
	var cbA, cbB string // countback pair: same handicap, same points, better back nine for cbA
	for _, f := range fl {
		for _, p := range asMaps(f["players"]) {
			rid := str(p["registrationId"])
			if _, ok := idx[rid]; !ok {
				idx[rid] = 100 + len(idx)
			}
		}
	}
	cbA, cbB = regs[40], regs[41]
	sysExec(t, inst, `UPDATE golf.tournament_scores SET playing_handicap = 10, course_handicap = 11 WHERE registration_id IN ($1, $2)`, mustUUID(cbA), mustUUID(cbB))
	hio := regs[50]
	card := func(rid string) []map[string]any {
		i := idx[rid]
		switch rid {
		case cbA: // bogeys early, pars late
			return trnEntries(pars, func(seq, par int) int {
				if seq <= 4 {
					return par + 2
				}
				return par
			})
		case cbB: // pars early, bogeys late
			return trnEntries(pars, func(seq, par int) int {
				if seq >= 15 {
					return par + 2
				}
				return par
			})
		case hio:
			return trnEntries(pars, func(seq, par int) int {
				if seq == 3 {
					return 1
				}
				return par + 1
			})
		}
		return trnEntries(pars, func(seq, par int) int { return trnStrokes(i, seq, par) })
	}
	// caddy: front nine online, back nine through the offline queue (replayed twice)
	cad.Must(200, "POST", trnBase+"/"+tid+"/scores", map[string]any{"registrationId": caddyPlayer, "entries": card(caddyPlayer)[:9], "deviceId": "TAB-T1"})
	lb := ga.Must(200, "GET", trnBase+"/"+tid+"/leaderboard?category=stableford&division=overall", nil).JSON()
	found := false
	for _, e := range trnBoard(t, lb, "stableford", "Overall") {
		if e["registrationId"] == caddyPlayer {
			found = e["thru"] == "9" && e["points"] != nil
		}
	}
	if !found {
		t.Fatalf("live leaderboard after the front nine: %v", lb)
	}
	back := card(caddyPlayer)[9:]
	for _, e := range back {
		e["clientAt"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
	}
	sItem := map[string]any{"id": newKey(), "action": "golf.tournament_score", "payload": map[string]any{"tournamentId": tid, "registrationId": caddyPlayer,
		"entries": back, "deviceId": "TAB-T1"}}
	for i := 0; i < 2; i++ {
		res := asMaps(cad.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{sItem}}).JSON()["results"])
		if (i == 0 && res[0]["status"] != "accepted") || (i == 1 && res[0]["status"] != "duplicate") {
			t.Fatalf("offline score replay %d: %v", i, res)
		}
	}
	// member enters own card in the Member App
	var memberReg string
	sysQueryRow(t, inst, `SELECT id::text FROM golf.tournament_registrations WHERE tournament_id = $1 AND customer_id = $2 AND status <> 'withdrawn'`,
		[]any{mustUUID(tid), mustUUID(memberCust)}, &memberReg)
	mc.Must(200, "POST", "/api/v1/member/golf/tournaments/"+tid+"/scores", map[string]any{"entries": card(memberReg), "deviceId": "APP"})
	if cs := mc.Must(200, "GET", "/api/v1/member/golf/tournaments/"+tid+"/my-scorecards", nil).Items(); len(cs) != 1 || cs[0]["gross"] == nil {
		t.Fatalf("my tournament scorecard: %v", cs)
	}
	// scoring desk: paper cards of everybody else
	scores := ga.Must(200, "GET", trnBase+"/"+tid+"/scores", nil).Items()
	if len(scores) != 72 {
		t.Fatalf("scorecards of the round: %d", len(scores))
	}
	scoreOf := map[string]string{}
	for _, s := range scores {
		rid := str(s["registrationId"])
		scoreOf[rid] = str(s["scoreId"])
		if rid == caddyPlayer || rid == memberReg {
			continue
		}
		ga.Must(200, "POST", trnBase+"/"+tid+"/scores", map[string]any{"registrationId": rid, "entries": card(rid)})
	}
	// DQ, WD, NR (one reinstated)
	ga.Must(200, "POST", trnBase+"/"+tid+"/scores/"+scoreOf[regs[60]]+":set-status", map[string]any{"status": "dq", "reason": "Wrong ball"})
	ga.Must(200, "POST", trnBase+"/"+tid+"/scores/"+scoreOf[regs[61]]+":set-status", map[string]any{"status": "wd", "reason": "Illness"})
	ga.Must(200, "POST", trnBase+"/"+tid+"/scores/"+scoreOf[regs[62]]+":set-status", map[string]any{"status": "nr", "reason": "No card"})
	ga.Must(200, "POST", trnBase+"/"+tid+"/scores/"+scoreOf[regs[62]]+":set-status", map[string]any{"status": "reinstate", "reason": "Card found"})
	// attest and validate every card (Tournament Policies: attestation first)
	ga.Must(409, "POST", trnBase+"/"+tid+"/scores/"+scoreOf[regs[1]]+":validate", nil)
	for rid, sid := range scoreOf {
		if rid == regs[60] || rid == regs[61] {
			continue
		}
		ga.Must(200, "POST", trnBase+"/"+tid+"/scores/"+sid+":attest", map[string]any{"attestedBy": "Marker"})
		ga.Must(200, "POST", trnBase+"/"+tid+"/scores/"+sid+":validate", nil)
	}
	// correction of a validated card (P2 rule) — a stroke more on hole 1
	cor := ga.Must(200, "POST", trnBase+"/"+tid+"/scores/"+scoreOf[regs[30]]+":correct", map[string]any{"reason": "Scorer error",
		"entries": []map[string]any{{"seq": 1, "strokes": trnStrokes(idx[regs[30]], 1, pars[0]) + 1}}}).JSON()
	if cor["status"] != "finalized" {
		t.Fatalf("corrected card: %v", cor)
	}
	ga.Must(422, "POST", trnBase+"/"+tid+"/scores/"+scoreOf[regs[30]]+":correct", map[string]any{"entries": []map[string]any{{"seq": 1, "strokes": 5}}})

	// Leaderboards: stableford & gross, overall and per division, countback.
	lb = ga.Must(200, "GET", trnBase+"/"+tid+"/leaderboard", nil).JSON()
	if lb["roundStatus"] != "completed" {
		t.Fatalf("round complete: %v", lb["roundStatus"])
	}
	stb := trnBoard(t, lb, "stableford", "Overall")
	pos := map[string]map[string]any{}
	for i, e := range stb {
		pos[str(e["registrationId"])] = e
		if i > 0 && e["status"] == "finished" && stb[i-1]["status"] == "finished" && e["points"].(float64) > stb[i-1]["points"].(float64) {
			t.Fatalf("stableford order: %v before %v", stb[i-1], e)
		}
	}
	if pos[regs[60]]["positionLabel"] != "DQ" || pos[regs[61]]["positionLabel"] != "WD" || pos[regs[62]]["status"] != "finished" {
		t.Fatalf("DQ / WD / reinstated: %v %v %v", pos[regs[60]], pos[regs[61]], pos[regs[62]])
	}
	a, b := pos[cbA], pos[cbB]
	if a["points"] != b["points"] || a["position"].(float64) >= b["position"].(float64) || a["tieBreak"] == nil || a["tied"] == true {
		t.Fatalf("countback last 9: %v vs %v", a, b)
	}
	trnBoard(t, lb, "gross", "Flight A")
	// Leaderboard Screen (role Screen) and the public leaderboard (consent).
	scr := roleUser(t, inst, "screen").Must(200, "GET", "/api/v1/golf/tournament-screen?tournamentId="+tid, nil).JSON()
	if len(asMaps(scr["leaderboards"])) != 1 || scr["rotateSeconds"].(float64) <= 0 {
		t.Fatalf("leaderboard screen: %v", scr)
	}
	plb := pub.Must(200, "GET", "/api/v1/public/tournaments/"+tid+"/leaderboard?propertyId="+pid, nil).JSON()
	masked := false
	for _, e := range trnBoard(t, plb, "stableford", "Overall") {
		if e["registrationId"] != nil {
			t.Fatal("public leaderboard exposes ids")
		}
		masked = masked || strings.HasPrefix(str(e["playerName"]), "Player ")
	}
	if !masked {
		t.Fatal("players without consent must be masked on the website")
	}
	if ml := mc.Must(200, "GET", "/api/v1/member/golf/tournaments/"+tid+"/leaderboard", nil).JSON(); len(asMaps(ml["boards"])) == 0 {
		t.Fatalf("member leaderboard: %v", ml)
	}

	// Special awards and the sponsorship invoice (FR-TRN-09/10).
	gm.Must(200, "POST", trnBase+"/"+tid+"/prizes/"+pNTP+":award", map[string]any{"registrationId": regs[7], "resultText": "1.35 m"})
	gm.Must(200, "POST", trnBase+"/"+tid+"/prizes/"+pLD+":award", map[string]any{"registrationId": regs[8], "resultText": "285 m"})
	fin := roleUser(t, inst, "finance_manager")
	inv := fin.Must(200, "POST", trnBase+"/"+tid+"/sponsors/"+sp+":invoice", map[string]any{"termsDays": 30}, "Idempotency-Key", newKey()).JSON()
	if inv["invoiceId"] == nil || inv["invoiceStatus"] == nil {
		t.Fatalf("sponsor invoice: %v", inv)
	}
	fin.Must(409, "POST", trnBase+"/"+tid+"/sponsors/"+sp+":invoice", map[string]any{})

	// Finalize: results locked, ranking prizes, HIO prize, Hall of Fame champions.
	ga.Must(403, "POST", trnBase+"/"+tid+":finalize", nil)
	res := gm.Must(200, "POST", trnBase+"/"+tid+":finalize", nil).JSON()
	if res["status"] != "completed" || len(asMaps(res["champions"])) < 4 {
		t.Fatalf("finalize: %v", res)
	}
	awarded := map[string]map[string]any{}
	for _, p := range asMaps(res["awards"]) {
		awarded[str(p["id"])] = p
	}
	for _, p := range []string{pGross, pStbA, pStbB, pNTP, pLD, pHIO} {
		if awarded[p] == nil {
			t.Fatalf("prize %s not awarded: %v", p, res["awards"])
		}
	}
	if awarded[pHIO]["registrationId"] != hio {
		t.Fatalf("hole-in-one prize: %v", awarded[pHIO])
	}
	winners := map[string]bool{}
	for _, p := range []string{pGross, pStbA, pStbB} {
		w := str(awarded[p]["registrationId"])
		if winners[w] {
			t.Fatalf("one ranking prize per player: %v", res["awards"])
		}
		winners[w] = true
	}
	gm.Must(200, "POST", trnBase+"/"+tid+"/prizes/"+pGross+":hand-over", map[string]any{"handedOverTo": "Winner"})
	ga.Must(409, "PATCH", trnBase+"/"+tid, map[string]any{"name": "Changed"})
	trnDispatch(t, "results published", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'golf.tournament_results_published' AND aggregate_id = $1`, []any{mustUUID(tid)}, &n)
		return n == 1
	})
	// Hall of Fame: Tournament Champion entries; public only with consent (P2).
	var champs, consented int
	sysQueryRow(t, inst, `SELECT count(*), count(*) FILTER (WHERE consent = 'granted') FROM golf.hall_of_fame h JOIN golf.tournament_results x ON x.id = h.source_id
		WHERE x.tournament_id = $1 AND h.category = 'tournament_champion'`, []any{mustUUID(tid)}, &champs, &consented)
	if champs < 3 {
		t.Fatalf("hall of fame champions: %d", champs)
	}
	hof := asMaps(sa.Must(200, "GET", "/api/v1/golf/hall-of-fame?limit=500", nil).JSON()["items"])
	for _, e := range hof {
		if strings.Contains(str(e["title"]), "Monthly Medal "+sfx) {
			sa.Must(204, "POST", "/api/v1/golf/hall-of-fame/"+str(e["id"])+":publish", nil)
		}
	}
	publicHOF := pub.Must(200, "GET", "/api/v1/public/hall-of-fame?propertyId="+pid, nil).Items()
	shown := 0
	for _, e := range publicHOF {
		if strings.Contains(str(e["title"]), "Monthly Medal "+sfx) {
			shown++
		}
	}
	if shown != consented {
		t.Fatalf("public champions %d, consented %d", shown, consented)
	}
	if r := mc.Must(200, "GET", "/api/v1/member/golf/tournaments/"+tid+"/results", nil).JSON(); len(asMaps(r["results"])) == 0 {
		t.Fatalf("member results: %v", r)
	}

	// Tournament Report & Golf Performance (FR-TRN-12, FR-RPT-P3-04).
	from, to := clubDay(inst, -1, func(time.Weekday) bool { return true }), day
	rep := gm.Must(200, "GET", "/api/v1/reporting/reports/golf.tournament?params[from]="+from+"&params[to]="+to, nil).JSON()
	var row map[string]any
	for _, r := range asMaps(rep["rows"]) {
		if r["code"] == tr["code"] {
			row = r
		}
	}
	if row == nil || row["participants"].(float64) != 72 || !dec(row["sponsorRevenue"]).Equal(dec("25000000")) || !dec(row["prizeCost"]).Equal(dec("59000000")) ||
		dec(row["feeRevenue"]).IsZero() || dec(row["packageRevenue"]).IsZero() || row["champion"] == nil {
		t.Fatalf("tournament report: %v", row)
	}
	dash := gm.Must(200, "GET", "/api/v1/reporting/dashboards/golf-performance?from="+from+"&to="+to, nil).JSON()
	kpi := map[string]string{}
	for _, k := range asMaps(dash["kpis"]) {
		kpi[str(k["key"])] = str(k["value"])
	}
	if dec(kpi["tournaments"]).IsZero() || dec(kpi["tournament_participants"]).LessThan(dec("72")) || dec(kpi["tournament_revenue"]).IsZero() ||
		dec(kpi["sponsor_revenue"]).LessThan(dec("25000000")) {
		t.Fatalf("golf performance tournament KPIs: %v", kpi)
	}
	// Customer 360 Tournament section.
	c360 := sa.Must(200, "GET", "/api/v1/crm/customers/"+memberCust+"/360", nil).JSON()
	if !strings.Contains(string(mustJSON(c360)), tid) {
		t.Fatalf("customer 360 tournament section: %v", c360["sections"])
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Multi-round tournament on tee times (two tees) with a cut, tee-off of
// flights, a standings draw for round 2 and the handicap maximum of
// Tournament Policies; cancellation of a tournament refunds paid players
// and re-opens the tee sheet.
func TestP3TournamentRoundsAndCancel(t *testing.T) {
	sa := superAdmin(t, inst)
	ga := roleUser(t, inst, "golf_admin")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	g := setupGolfCourse(t, sa, "TS"+sfx[:3])
	pars := trnPars(g.Holes)
	d1 := clubDay(inst, 15, isSaturday)
	d1t, _ := time.Parse("2006-01-02", d1)
	d2 := d1t.AddDate(0, 0, 1).Format("2006-01-02")
	trnEnablePayments(t)

	// Tournament Policies: refuse players above the maximum handicap.
	pol := func(mode string) {
		sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Tournament Policies", "code": "golf.tournament",
			"name": "Tournament Policies", "value": map[string]any{"registrationDeadlineDays": 1, "selfWithdrawalCutoffHours": 24, "paymentDueHours": 24,
				"fullRefundDays": 7, "partialRefundDays": 3, "partialRefundPercent": "50", "handicapAllowancePercent": "95", "maxHandicap": "36",
				"maxHandicapLadies": "40", "handicapLimitMode": mode, "handicapSource": "federation_first", "requireHandicap": false, "tieBreak": "countback",
				"waitlistAutoPromote": true, "waitlistMax": 0, "requireAttestation": false, "onePrizePerPlayer": true, "roundDurationMinutes": 330,
				"blockLeadMinutes": 30, "leaderboardRotateSeconds": 15, "leaderboardScreenRows": 20, "defaultFormat": "stableford", "defaultStartTime": "07:00"}})
	}
	pol("reject")
	t.Cleanup(func() { pol("cap") })

	tr := ga.Must(201, "POST", trnBase, map[string]any{"name": "Club Championship " + sfx, "tournamentType": "club_championship", "courseId": g.Course,
		"playingRouteId": g.RouteAB, "format": "stroke_play", "scoringBasis": "gross", "fieldSize": 8, "startType": "tee_times", "cutAfterRound": 1, "cutTop": 4,
		"rounds": []map[string]any{{"playDate": d1, "startTime": "07:00", "startTees": "1,10", "teeIntervalMinutes": 10}, {"playDate": d2, "startTime": "07:30"}}}).JSON()
	tid := str(tr["id"])
	ga.Must(201, "POST", trnBase+"/"+tid+"/fees", map[string]any{"component": "entry_fee", "name": "Entry", "amount": "300000"})
	ga.Must(200, "POST", trnBase+"/"+tid+":open-registration", map[string]any{})
	if r := ga.Do("POST", trnBase+"/"+tid+"/registrations", map[string]any{"guest": map[string]any{"name": "High Handicap " + sfx, "phone": "+62844" + sfx + "9",
		"gender": "male"}, "handicapIndex": "40"}); r.Status != 409 || !strings.Contains(string(r.Body), "handicap_above_maximum") {
		t.Fatalf("maximum handicap: %s", r)
	}
	var regs []string
	for i := 0; i < 6; i++ {
		r := ga.Must(201, "POST", trnBase+"/"+tid+"/registrations", map[string]any{"guest": map[string]any{"name": fmt.Sprintf("Champ %d %s", i, sfx),
			"phone": fmt.Sprintf("+62844%s%d", sfx, i)}, "handicapIndex": fmt.Sprint(i * 3)}).JSON()
		regs = append(regs, str(r["id"]))
	}
	// round 1: random draw over two tees, flights tee off one by one
	dr := ga.Must(200, "POST", trnBase+"/"+tid+"/flights:generate", map[string]any{"method": "random", "playersPerFlight": 3}).JSON()
	fl := asMaps(dr["flights"])
	if len(fl) != 2 || fl[0]["startLabel"] == fl[1]["startLabel"] || fl[0]["startAt"] != fl[1]["startAt"] {
		t.Fatalf("two-tee start: %v", fl)
	}
	ga.Must(200, "PATCH", trnBase+"/"+tid+"/flights/"+str(fl[1]["id"]), map[string]any{"startAt": d1t.Add(time.Hour).Format(time.RFC3339)})
	ga.Must(200, "POST", trnBase+"/"+tid+":publish-draw", map[string]any{"notify": false})
	ga.Must(409, "POST", trnBase+"/"+tid+"/flights/"+str(fl[0]["id"])+":tee-off", nil)
	ga.Must(200, "POST", trnBase+"/"+tid+":start", nil)
	for _, f := range fl {
		if r := ga.Must(200, "POST", trnBase+"/"+tid+"/flights/"+str(f["id"])+":tee-off", nil).JSON(); r["status"] != "in_play" {
			t.Fatalf("tee-off: %v", r)
		}
	}
	play := func(round int, over func(i int) int) {
		for i, rid := range regs {
			r := ga.Do("POST", trnBase+"/"+tid+"/scores", map[string]any{"registrationId": rid, "round": round,
				"entries": trnEntries(pars, func(seq, par int) int {
					if seq <= over(i) {
						return par + 1
					}
					return par
				})})
			if r.Status == 404 { // missed the cut: no card in round 2
				continue
			}
			if r.Status != 200 {
				t.Fatalf("round %d score: %s", round, r)
			}
			sid := str(r.JSON()["scoreId"])
			ga.Must(200, "POST", trnBase+"/"+tid+"/scores/"+sid+":validate", nil)
		}
	}
	play(1, func(i int) int { return i }) // player i is i over par
	// round 2: standings draw, the cut (top 4) applied
	dr2 := ga.Must(200, "POST", trnBase+"/"+tid+"/flights:generate", map[string]any{"round": 2}).JSON()
	if ex, _ := dr2["excluded"].([]any); len(ex) != 2 {
		t.Fatalf("cut: %v", dr2["excluded"])
	}
	n := 0
	for _, f := range asMaps(dr2["flights"]) {
		n += len(asMaps(f["players"]))
	}
	if n != 4 {
		t.Fatalf("players after the cut: %d", n)
	}
	ga.Must(200, "POST", trnBase+"/"+tid+":publish-draw", map[string]any{"round": 2, "notify": false})
	ga.Must(200, "POST", trnBase+"/"+tid+":start", nil)
	for _, f := range asMaps(dr2["flights"]) {
		ga.Must(200, "POST", trnBase+"/"+tid+"/flights/"+str(f["id"])+":tee-off", nil)
	}
	play(2, func(i int) int { return 0 })
	lb := ga.Must(200, "GET", trnBase+"/"+tid+"/leaderboard?category=gross", nil).JSON()
	e := trnBoard(t, lb, "gross", "Overall")
	if e[0]["registrationId"] != regs[0] || e[len(e)-1]["positionLabel"] != "MC" || len(asMaps(e[0]["rounds"])) != 2 {
		t.Fatalf("multi-round leaderboard: %v", e)
	}
	res := ga.Must(403, "POST", trnBase+"/"+tid+":finalize", nil)
	_ = res
	fr := roleUser(t, inst, "golf_manager").Must(200, "POST", trnBase+"/"+tid+":finalize", nil).JSON()
	if c := asMaps(fr["champions"]); len(c) != 1 || c[0]["category"] != "gross" {
		t.Fatalf("club champion: %v", fr["champions"])
	}
	var clubChamp int
	sysQueryRow(t, inst, `SELECT count(*) FROM golf.hall_of_fame h JOIN golf.tournament_results x ON x.id = h.source_id WHERE x.tournament_id = $1
		AND h.category = 'club_champion' AND h.division = 'open'`, []any{mustUUID(tid)}, &clubChamp)
	if clubChamp != 1 {
		t.Fatalf("club champion entry: %d", clubChamp)
	}

	// Cancellation: paid players refunded in full, tee sheet re-opened.
	c := ga.Must(201, "POST", trnBase, map[string]any{"name": "Rain Cup " + sfx, "courseId": g.Course, "format": "stableford", "fieldSize": 4,
		"rounds": []map[string]any{{"playDate": d1, "startTime": "13:00"}}}).JSON()
	cid := str(c["id"])
	ga.Must(201, "POST", trnBase+"/"+cid+"/fees", map[string]any{"component": "entry_fee", "name": "Entry", "amount": "250000"})
	ga.Must(200, "POST", trnBase+"/"+cid+":open-registration", map[string]any{})
	cr := ga.Must(201, "POST", trnBase+"/"+cid+"/registrations", map[string]any{"guest": map[string]any{"name": "Rainy " + sfx, "phone": "+62833" + sfx},
		"payment": "pay_later"}).JSON()
	ga.Must(200, "POST", trnBase+"/"+cid+"/registrations/"+str(cr["id"])+":pay", map[string]any{"methodType": "cash"})
	// the member registers in the Member App (member charge) and withdraws himself (full refund)
	mc, _ := trnMemberClient(t, sa)
	mreg := mc.Must(201, "POST", "/api/v1/member/golf/tournaments/"+cid+"/registrations", map[string]any{"payment": "online", "paymentMethod": "qris"}).JSON()
	var mrid string
	for _, x := range mc.Must(200, "GET", "/api/v1/member/golf/my-tournaments", nil).Items() {
		if x["tournamentId"] == cid {
			mrid = str(x["registrationId"])
		}
	}
	if mrid == "" || mreg["status"] != "registered" {
		t.Fatalf("member registration: %v", mreg)
	}
	if w := mc.Must(200, "POST", "/api/v1/member/golf/my-tournaments/"+mrid+":withdraw", map[string]any{"reason": "Travel"}).JSON(); w["status"] != "withdrawn" {
		t.Fatalf("member withdrawal: %v", w)
	}
	ga.Must(422, "POST", trnBase+"/"+cid+":cancel", map[string]any{})
	cc := ga.Must(200, "POST", trnBase+"/"+cid+":cancel", map[string]any{"reason": "Course flooded"}).JSON()
	if cc["status"] != "cancelled" || asMaps(cc["rounds"])[0]["courseBlockId"] != nil {
		t.Fatalf("cancelled: %v", cc)
	}
	rr := ga.Must(200, "GET", trnBase+"/"+cid+"/registrations/"+str(cr["id"]), nil).JSON()
	if rr["status"] != "withdrawn" || rr["paymentStatus"] != "refunded" || !dec(rr["refundAmount"]).Equal(dec("250000")) {
		t.Fatalf("refund on cancellation: %v", rr)
	}
	if ls := ga.Must(200, "GET", trnBase+"?filter[status]=cancelled", nil).Items(); !containsID(ls, cid) {
		t.Fatal("schedule filter")
	}
}

// A corporate tournament sold through CRM Sales: the accepted quotation of
// the line "tournament" becomes one draft tournament with the payment
// schedule of the quotation (FR-QUO-06/07); the banquet event of the
// tournament dinner is followed through banquet events (FR-TRN-13).
func TestP3TournamentCorporate(t *testing.T) {
	sa := superAdmin(t, inst)
	sx := roleUser(t, inst, "sales_executive")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	setupGolfCourse(t, sa, "TQ"+sfx[:3])
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "TRNQ" + sfx, "name": "PT Corporate Golf " + sfx}))
	cust := customer(t, sa, "TRNQC"+sfx, "Corporate Host "+sfx, map[string]any{"email": "host" + sfx + "@corp.test"})
	ev := clubDay(inst, 40, isWeekday)
	q := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "corporateAccountId": corp, "title": "Corporate Golf Day " + sfx,
		"line": "tournament", "eventType": "tournament", "eventDate": ev, "pax": 40, "pricingMode": "nett",
		"lines": []map[string]any{{"itemType": "service", "description": "Corporate tournament package", "quantity": "40", "unitPrice": "1500000"}}},
		"Idempotency-Key", newKey()).JSON()
	sx.Must(200, "POST", "/api/v1/crm/quotations/"+str(q["id"])+":accept", map[string]any{"acceptedByName": "Host", "note": "Signed"})
	var tour map[string]any
	ga := roleUser(t, inst, "golf_manager")
	trnDispatch(t, "corporate tournament", func() bool {
		for _, x := range ga.Must(200, "GET", trnBase+"?filter[type]=corporate", nil).Items() {
			if x["quotationNumber"] == q["number"] {
				tour = x
			}
		}
		return tour != nil
	})
	if tour["status"] != "draft" || tour["fieldSize"].(float64) != 40 || tour["startDate"] != ev || tour["corporateAccountId"] != corp {
		t.Fatalf("tournament from the quotation: %v", tour)
	}
	// a second delivery creates nothing new
	var n, schedules int
	sysExec(t, inst, `INSERT INTO platform.outbox (id, event_type, aggregate_type, aggregate_id, property_id, payload)
		SELECT $2, event_type, aggregate_type, aggregate_id, property_id, payload FROM platform.outbox
		WHERE event_type = 'crm.quotation_accepted' AND payload->>'quotationId' = $1 LIMIT 1`, str(q["id"]), uuid.New())
	trnDispatch(t, "redelivery", func() bool {
		var pending int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'crm.quotation_accepted' AND payload->>'quotationId' = $1 AND dispatched_at IS NULL`,
			[]any{str(q["id"])}, &pending)
		return pending == 0
	})
	sysQueryRow(t, inst, `SELECT count(*) FROM golf.tournaments WHERE quotation_id = $1`, []any{mustUUID(str(q["id"]))}, &n)
	sysQueryRow(t, inst, `SELECT count(*) FROM billing.payment_schedules WHERE source_type = 'tournament' AND source_id = $1`, []any{mustUUID(str(tour["id"]))}, &schedules)
	if n != 1 || schedules != 1 {
		t.Fatalf("idempotent conversion: %d tournaments, %d schedules", n, schedules)
	}
	sysQueryRow(t, inst, `SELECT count(*) FROM billing.payment_schedules WHERE source_type = 'quotation' AND source_ref = $1`, []any{q["number"]}, &schedules)
	if schedules != 0 {
		t.Fatalf("the generic quotation schedule must not be issued: %d", schedules)
	}

	// Banquet event of the tournament dinner (snapshot through events).
	evID := uuid.New()
	ga.Must(200, "PATCH", trnBase+"/"+str(tour["id"]), map[string]any{"eventId": evID})
	payload, _ := json.Marshal(map[string]any{"eventId": evID, "number": "EVT-" + sfx, "eventType": "tournament", "title": "Gala Dinner", "status": "definite"})
	sysExec(t, inst, `INSERT INTO platform.outbox (id, event_type, aggregate_type, aggregate_id, property_id, payload) VALUES ($1, 'banquet.event_confirmed',
		'banquet.event', $2, $3, $4)`, uuid.New(), evID, inst.Main, payload)
	trnDispatch(t, "event snapshot", func() bool {
		var num string
		sysQueryRow(t, inst, `SELECT coalesce(event_number, '') FROM golf.tournaments WHERE id = $1`, []any{mustUUID(str(tour["id"]))}, &num)
		return num == "EVT-"+sfx
	})
}

// FR-MIG-P3-03: tournament history and results import (preview, commit,
// idempotent re-run) into a read-only archive with Hall of Fame champions.
func TestP3TournamentHistoryImport(t *testing.T) {
	sa := superAdmin(t, inst)
	gm := roleUser(t, inst, "golf_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	csv := "tournamentRef,tournamentName,startDate,endDate,tournamentType,format,category,division,hallOfFameDivision,position,playerName,score,toPar,publicConsent\n" +
		"RH-" + sfx + ",Club Championship 2025,2025-08-16,2025-08-17,club_championship,stroke_play,gross,,,1,Budi Juara " + sfx + ",142,-2,true\n" +
		"RH-" + sfx + ",Club Championship 2025,2025-08-16,2025-08-17,club_championship,stroke_play,gross,,,2,Andi Kedua " + sfx + ",145,+1,false\n" +
		"RH-" + sfx + ",Club Championship 2025,2025-08-16,2025-08-17,club_championship,stroke_play,net,Ladies,ladies,1,Sari Ladies " + sfx + ",70,,false\n" +
		"RH-" + sfx + ",Club Championship 2025,2025-08-16,2025-08-17,club_championship,stroke_play,putts,,,1,Wrong " + sfx + ",1,,\n"
	pv := gm.Must(200, "POST", trnBase+":import", map[string]any{"mode": "preview", "csv": csv}).JSON()
	if pv["results"].(float64) != 3 || pv["failed"].(float64) != 1 || pv["tournaments"].(float64) != 1 {
		t.Fatalf("preview: %v", pv)
	}
	if ls := gm.Must(200, "GET", trnBase+"?filter[source]=import&q=Club+Championship+2025", nil).Items(); len(ls) != 0 {
		var mine int
		for _, x := range ls {
			if strings.Contains(str(x["code"]), sfx) {
				mine++
			}
		}
		if mine != 0 {
			t.Fatal("preview saved data")
		}
	}
	for i := 0; i < 2; i++ {
		res := gm.Must(200, "POST", trnBase+":import", map[string]any{"mode": "commit", "csv": csv}).JSON()
		if res["results"].(float64) != 3 || res["champions"].(float64) != 2 {
			t.Fatalf("commit %d: %v", i, res)
		}
	}
	var tid string
	sysQueryRow(t, inst, `SELECT id::text FROM golf.tournaments WHERE legacy_ref = $1`, []any{"RH-" + sfx}, &tid)
	r := gm.Must(200, "GET", trnBase+"/"+tid+"/results", nil).JSON()
	if r["status"] != "completed" || len(asMaps(r["results"])) != 3 || len(asMaps(r["champions"])) != 2 {
		t.Fatalf("archived results: %v", r)
	}
	gm.Must(409, "POST", trnBase+"/"+tid+"/registrations", map[string]any{"guest": map[string]any{"name": "x", "phone": "+6281"}})
	var entries, public int
	sysQueryRow(t, inst, `SELECT count(*), count(*) FILTER (WHERE consent = 'granted') FROM golf.hall_of_fame h JOIN golf.tournament_results x ON x.id = h.source_id
		WHERE x.tournament_id = $1 AND h.category = 'club_champion'`, []any{mustUUID(tid)}, &entries, &public)
	if entries != 2 || public != 1 {
		t.Fatalf("hall of fame from history: %d entries, %d consented", entries, public)
	}
	_ = sa
}
