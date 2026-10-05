package e2e

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/crm/journey"
)

// PRD P5 EP-17–20 Advanced CRM & Loyalty: tier programme (annual
// evaluation, grace, benefits incl. the golf booking window), reward
// eligibility, Top Spender programmes and budget, journeys (renewal AC,
// consent / frequency cap / quiet hours, A/B and control group, offers),
// segmentation & VIP, CRM analytics and reports.

func init() {
	resourceCRUDSkip["crm.loyalty_reward_rule"] = "covered by TestP5CRMRewards"
	resourceCRUDSkip["crm.top_spender_program"] = "a reward or an invitation list is required; covered by TestP5CRMRewards"
}

func p5cSfx() string { return fmt.Sprint(time.Now().UnixNano() % 1e7) }

// p5cTiers creates a Silver / Gold / Platinum set ranked above every other
// tier of the shared instance (PRD P5 §16 #14) and archives it at the end.
func p5cTiers(t *testing.T, sa *Client, sfx string) (silver, gold, plat string) {
	t.Helper()
	p5tIsolateTiers(t) // the tier ladder is validated against the active tiers (p5_tiers_test.go)
	mk := func(code, name string, rank int, spend, mult, disc string, window int, event bool) string {
		return idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/tiers", map[string]any{"code": code + sfx, "name": name + " " + sfx, "rank": rank,
			"minSpend": spend, "multiplier": mult, "fnbDiscountPercent": disc, "bookingWindowDays": window, "eventAccess": event, "priorityService": event}))
	}
	silver = mk("P5S", "Silver", 9001, "0", "1", "0", 0, false)
	gold = mk("P5G", "Gold", 9002, "25000000", "1.25", "5", 2, false)
	plat = mk("P5P", "Platinum", 9003, "75000000", "1.5", "10", 4, true)
	t.Cleanup(func() { // tiers in use cannot be deleted: deactivate them
		for _, id := range []string{plat, gold, silver} {
			sa.Do("PATCH", "/api/v1/crm/loyalty/tiers/"+id, map[string]any{"status": "inactive"})
		}
	})
	return silver, gold, plat
}

// p5cEarnRule gives walk-in charges (line other) 1 point per Rp10.000 for
// the test and archives the rule at the end.
func p5cEarnRule(t *testing.T, sa *Client, sfx string) {
	t.Helper()
	rid := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/earning-rules", map[string]any{"code": "P5R" + sfx, "name": "P5 walk-in spend",
		"ruleType": "spend", "businessLine": "other", "amountPerPoint": "10000", "priority": 1}))
	t.Cleanup(func() { sa.Do("PATCH", "/api/v1/crm/loyalty/earning-rules/"+rid, map[string]any{"status": "inactive"}) })
}

// p5cPay charges and pays a walk-in folio of a customer.
func p5cPay(t *testing.T, sa *Client, cust, desc, amount string) map[string]any {
	t.Helper()
	folio := engFolio(t, sa, cust, desc, amount)
	return sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": folio, "amount": amount, "methodType": "cash", "channel": "venue"}).JSON()
}

func p5cCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	sysQueryRow(t, inst, sql, args, &n)
	return n
}

// p5cQuietAway moves the quiet hours of the journeys away from now (the
// suite runs at any time of day) and restores the policy at the end.
func p5cQuietAway(t *testing.T, sa *Client, mutate func(v map[string]any)) {
	t.Helper()
	now := time.Now().In(clubLoc(inst))
	fixPolicy(t, sa, journey.PolicyCode, func(v map[string]any) {
		v["quietStart"], v["quietEnd"] = now.Add(6*time.Hour).Format("15:04"), now.Add(7*time.Hour).Format("15:04")
		if mutate != nil {
			mutate(v)
		}
	})
}

// p5cIsolate pauses the other active journeys (demo priority journeys) so
// they do not message the test customers, and resumes them at the end.
func p5cIsolate(t *testing.T, sa *Client) {
	t.Helper()
	var paused []string
	for _, j := range sa.Must(200, "GET", "/api/v1/crm/journeys?filter[status]=active&limit=500", nil).Items() {
		sa.Must(200, "POST", "/api/v1/crm/journeys/"+str(j["id"])+":pause", nil)
		paused = append(paused, str(j["id"]))
	}
	t.Cleanup(func() {
		for _, id := range paused {
			sa.Do("POST", "/api/v1/crm/journeys/"+id+":activate", nil)
		}
	})
}

func p5cRun(t *testing.T, c *Client, jid string) map[string]any {
	t.Helper()
	return c.Must(200, "POST", "/api/v1/crm/journeys/"+jid+":run", nil).JSON()
}

func p5cEnrollment(t *testing.T, c *Client, jid, cust string) map[string]any {
	t.Helper()
	l := c.Must(200, "GET", "/api/v1/crm/journeys/"+jid+"/enrollments?filter[customerId]="+cust, nil).Items()
	if len(l) != 1 {
		t.Fatalf("enrollments of %s in %s: %v", cust, jid, l)
	}
	return l[0]
}

// p5cSteps returns "step:outcome" of an enrollment in order.
func p5cSteps(t *testing.T, c *Client, jid, enrollment string) []string {
	t.Helper()
	var out []string
	for _, e := range c.Must(200, "GET", "/api/v1/crm/journeys/"+jid+"/events?filter[enrollmentId]="+enrollment, nil).Items() {
		out = append(out, str(e["stepKey"])+":"+str(e["outcome"]))
	}
	return out
}

// p5cDue makes a waiting enrollment due now (the wait has passed).
func p5cDue(t *testing.T, enrollment string) {
	t.Helper()
	sysExec(t, inst, `UPDATE crm.journey_enrollments SET next_run_at = now() - interval '1 second' WHERE id = $1`, mustUUID(enrollment))
}

func p5cOptIn(t *testing.T, sa *Client, cust string) {
	t.Helper()
	sa.Must(200, "POST", "/api/v1/crm/customers/"+cust+"/communication-preferences", map[string]any{"whatsapp": true, "email": true, "inApp": true})
}

func p5cJourney(t *testing.T, sa *Client, body map[string]any) map[string]any {
	t.Helper()
	j := sa.Must(201, "POST", "/api/v1/crm/journeys", body).JSON()
	j = sa.Must(200, "POST", "/api/v1/crm/journeys/"+str(j["id"])+":activate", nil).JSON()
	if j["status"] != "active" {
		t.Fatalf("journey activation without a workflow: %v", j["status"])
	}
	return j
}

func p5cMsg(key, channel, body string, extra map[string]any) map[string]any {
	s := map[string]any{"key": key, "stepType": "message", "name": key, "channel": channel, "subject": "S " + key, "body": body}
	for k, v := range extra {
		s[k] = v
	}
	return s
}

// EP-18 AC: a member reaching the Gold threshold at the end of the period
// is upgraded automatically, notified, and the new multiplier applies to the
// next transaction. Also: tier benefits (booking window +days, F&B
// discount, event access) through the public interface and honoured by the
// golf booking window, annual evaluation with a 3-month grace before the
// downgrade, grace review, tier evaluation runs, crm.tier_evaluated and
// the Member App tier progress.
func TestP5CRMTierProgram(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := p5cSfx()
	silver, gold, plat := p5cTiers(t, sa, sfx)
	p5cEarnRule(t, sa, sfx)
	ana := customer(t, sa, "P5TA"+sfx, "Ana Gold "+sfx, map[string]any{"email": "ana" + sfx + "@p5.test", "phone": "+62813" + sfx})

	// Spend Rp26 jt during the period while the tier is held (no upgrade on earning).
	acct := sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": ana, "tierId": silver}).JSON()
	aid := str(acct["id"])
	sa.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":set-tier", map[string]any{"tierId": silver, "lock": true, "reason": "Hold for evaluation"})
	p5cPay(t, sa, ana, "Golf day "+sfx, "26000000")
	engDispatch(t, "points earned", func() bool { return engBalance(t, sa, aid) == 2600 })
	if a := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+aid, nil).JSON(); str(a["tierId"]) != silver {
		t.Fatalf("locked tier changed: %v", a["tierId"])
	}
	sa.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":set-tier", map[string]any{"tierId": silver, "lock": false, "reason": "Evaluate normally"})

	// End of the period: the annual evaluation upgrades to Gold.
	sa.Must(422, "POST", "/api/v1/crm/loyalty/tier-evaluations", map[string]any{"kind": "monthly"})
	run := sa.Must(201, "POST", "/api/v1/crm/loyalty/tier-evaluations", map[string]any{"kind": "annual", "accountIds": []string{aid}}).JSON()
	if run["accounts"].(float64) != 1 || run["upgraded"].(float64) != 1 {
		t.Fatalf("annual evaluation: %v", run)
	}
	det := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-evaluations/"+str(run["id"]), nil).JSON()
	line := det["lines"].([]any)[0].(map[string]any)
	if line["outcome"] != "upgraded" || str(line["toTierId"]) != gold || dec(line["spendBasis"]).IntPart() != 26000000 {
		t.Fatalf("evaluation line: %v", line)
	}
	if a := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+aid, nil).JSON(); str(a["tierId"]) != gold || a["tierMultiplier"] != "1.25" {
		t.Fatalf("upgraded account: %v", a)
	}
	if !containsID(sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-evaluations?filter[kind]=annual", nil).Items(), str(run["id"])) {
		t.Fatal("tier evaluation list")
	}
	// Notified (tier changed) and crm.tier_evaluated published.
	engDispatch(t, "tier notification", func() bool {
		return p5cCount(t, `SELECT count(*) FROM platform.notification_deliveries WHERE event_code = 'crm.loyalty_tier_changed' AND recipient = $1`,
			"ana"+sfx+"@p5.test") > 0
	})
	if n := pcEvents(t, "crm.tier_evaluated", "accountId", aid); n != 1 {
		t.Fatalf("crm.tier_evaluated events: %d", n)
	}
	// The next transaction earns with the Gold multiplier: Rp1 jt → 100 × 1.25.
	p5cPay(t, sa, ana, "Dinner "+sfx, "1000000")
	engDispatch(t, "gold points", func() bool { return engBalance(t, sa, aid) == 2600+125 })

	// Benefits through the public interface (FR-LOY-P5-02).
	b := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-benefits?customerId="+ana, nil).JSON()
	if b["bookingWindowDays"].(float64) != 2 || b["fnbDiscountPercent"] != "5" || b["pointsMultiplier"] != "1.25" || b["eventAccess"] != false {
		t.Fatalf("gold benefits: %v", b)
	}
	sa.Must(422, "GET", "/api/v1/crm/loyalty/tier-benefits", nil)

	// Below the threshold at the annual evaluation: 3 months grace, then the downgrade.
	budi := customer(t, sa, "P5TB"+sfx, "Budi Platinum "+sfx, map[string]any{"email": "budi" + sfx + "@p5.test"})
	bid := str(sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": budi, "tierId": plat}).JSON()["id"])
	g := sa.Must(201, "POST", "/api/v1/crm/loyalty/tier-evaluations", map[string]any{"kind": "annual", "accountIds": []string{bid}}).JSON()
	if g["graceStarted"].(float64) != 1 {
		t.Fatalf("grace: %v", g)
	}
	want := time.Now().In(clubLoc(inst)).AddDate(0, 3, 0).Format("2006-01-02")
	bb := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-benefits?customerId="+budi, nil).JSON()
	if str(bb["tierId"]) != plat || str(bb["graceUntil"]) != want || bb["bookingWindowDays"].(float64) != 4 || bb["eventAccess"] != true {
		t.Fatalf("platinum in grace: %v (want grace until %s)", bb, want)
	}
	// The periodic run (P3 Evaluate Tiers) keeps the tier during the grace.
	sa.Must(200, "POST", "/api/v1/crm/loyalty/tiers:evaluate", nil)
	if a := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+bid, nil).JSON(); str(a["tierId"]) != plat {
		t.Fatalf("periodic evaluation downgraded during grace: %v", a["tierId"])
	}
	engDispatch(t, "grace notice", func() bool {
		return p5cCount(t, `SELECT count(*) FROM platform.notification_deliveries WHERE event_code = 'crm.loyalty_tier_grace' AND recipient = $1`,
			"budi"+sfx+"@p5.test") > 0
	})
	sysExec(t, inst, `UPDATE crm.loyalty_accounts SET grace_until = $2::date - 1 WHERE id = $1`, mustUUID(bid), engToday())
	gr := sa.Must(201, "POST", "/api/v1/crm/loyalty/tier-evaluations", map[string]any{"kind": "grace_review", "accountIds": []string{bid}}).JSON()
	if gr["downgraded"].(float64) != 1 {
		t.Fatalf("grace review: %v", gr)
	}
	if a := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-benefits?customerId="+budi, nil).JSON(); str(a["tierId"]) != silver || a["graceUntil"] != nil {
		t.Fatalf("after the grace: %v", a)
	}
	if rows := engReport(t, sa, "crm.tier_evaluation", "params[outcome]=downgraded"); len(rows) == 0 {
		t.Fatal("Tier Evaluation Report")
	}
	if rows := engReport(t, sa, "crm.loyalty_tier", ""); len(rows) == 0 {
		t.Fatal("Loyalty Tier Report")
	}

	// Member App tier progress (EP-26).
	mc := roleUser(t, inst, "member")
	me := memberCustomer(t, sa)
	if h := mc.Must(200, "GET", "/api/v1/member/loyalty", nil).JSON(); h["enrolled"] != true {
		mc.Must(200, "POST", "/api/v1/member/loyalty:join", nil)
	}
	var macct string
	sysQueryRow(t, inst, `SELECT id::text FROM crm.loyalty_accounts WHERE customer_id = $1`, []any{mustUUID(me)}, &macct)
	sa.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+macct+":set-tier", map[string]any{"tierId": silver, "reason": "P5 progress test"})
	pr := mc.Must(200, "GET", "/api/v1/member/loyalty/progress", nil).JSON()
	if pr["enrolled"] != true || pr["nextTier"].(map[string]any)["id"] != gold || pr["nextTierBenefits"].(map[string]any)["bookingWindowDays"].(float64) != 2 ||
		pr["currentTierStatus"] != "qualified" || pr["spendToNext"] == nil {
		t.Fatalf("tier progress: %v", pr)
	}
	mc.Must(403, "GET", "/api/v1/crm/loyalty/tier-evaluations", nil)
}

// FR-LOY-P5-02: the booking window +days of the tier is honoured by the
// golf booking of the Member App (golf reads the benefit through the hook
// wired by internal/app).
func TestP5CRMTierBookingWindow(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := p5cSfx()
	_, _, plat := p5cTiers(t, sa, sfx)
	m := anon(t, inst)
	m.Must(202, "POST", "/api/v1/auth/otp/request", map[string]any{"email": "member@demo.oneclub.id", "channel": "email"})
	var body string
	waitFor(t, 10*time.Second, "login code", func() bool {
		return p5cCount(t, `SELECT count(*) FROM platform.notification_deliveries d JOIN platform.users u ON u.id = d.user_id
			WHERE u.email = 'member@demo.oneclub.id' AND d.event_code = 'auth.login_code' AND d.created_at > now() - interval '1 minute'`) > 0
	})
	sysQueryRow(t, inst, `SELECT d.body FROM platform.notification_deliveries d JOIN platform.users u ON u.id = d.user_id
		WHERE u.email = 'member@demo.oneclub.id' AND d.event_code = 'auth.login_code' AND d.channel = 'email' ORDER BY d.created_at DESC LIMIT 1`, nil, &body)
	code := regexp.MustCompile(`\b(\d{6})\b`).FindStringSubmatch(body)
	if code == nil {
		t.Fatalf("no code in %q", body)
	}
	m.Must(200, "POST", "/api/v1/auth/otp/verify", map[string]any{"email": "member@demo.oneclub.id", "code": code[1]})

	course := demoCourse(t, inst)
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	day := time.Now().In(clubLoc(inst)).AddDate(0, 0, 16).Format("2006-01-02") // beyond the 14-day member window
	slots := slotsOf(teeTimes(t, gm, course, day), "afternoon", 1)
	if len(slots) < 4 {
		t.Fatalf("afternoon slots on %s: %d", day, len(slots))
	}
	slot := slots[len(slots)-3]["id"]
	if r := m.Do("POST", "/api/v1/member/golf/holds", map[string]any{"teeTimeId": slot, "players": 2}); r.Status != 409 ||
		!strings.Contains(string(r.Body), "outside_booking_window") {
		t.Fatalf("16 days ahead without a tier benefit: %s", r)
	}
	// Platinum: booking window + 4 days.
	member := demoMemberCustomer(t, inst)
	var acct string
	sysQueryRow(t, inst, `SELECT coalesce((SELECT id::text FROM crm.loyalty_accounts WHERE customer_id = $1), '')`, []any{mustUUID(member)}, &acct)
	if acct == "" {
		acct = str(sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": member}).JSON()["id"])
	}
	before := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+acct, nil).JSON()
	sa.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+acct+":set-tier", map[string]any{"tierId": plat, "reason": "P5 booking window test"})
	t.Cleanup(func() {
		sa.Do("POST", "/api/v1/crm/loyalty/accounts/"+acct+":set-tier", map[string]any{"tierId": before["tierId"], "lock": before["tierLocked"],
			"reason": "restore after the P5 booking window test"})
	})
	h := m.Must(201, "POST", "/api/v1/member/golf/holds", map[string]any{"teeTimeId": slot, "players": 2}).JSON()
	bk := m.Must(201, "POST", "/api/v1/member/golf/bookings", map[string]any{"bookingType": "member", "holdId": h["id"], "teeTimeId": slot,
		"players": []map[string]any{{"playerType": "member", "memberNo": "D0001"},
			{"playerType": "guest_of_member", "name": "Tamu P5 " + sfx, "phone": "+62812" + sfx, "hostIndex": 0}}}).JSON()
	if bk["status"] != "confirmed" {
		t.Fatalf("booking within the Platinum window: %v", bk["status"])
	}
	m.Must(200, "POST", "/api/v1/member/bookings/"+str(bk["id"])+":cancel", map[string]any{"reason": "P5 test"})
}

// FR-LOY-P5-03..06: reward catalogue with cost, reward eligibility rules
// (tier, limit per customer), eligible rewards (staff and Member App),
// rewards issued without points (staff, Top Spender programme) with
// crm.reward_issued, the loyalty budget of 2% net revenue enforced on
// automatic rewards, and the Reward Redemption / Loyalty Programme Cost
// reports.
func TestP5CRMRewards(t *testing.T) {
	sa := superAdmin(t, inst)
	fd := roleUser(t, inst, "front_desk")
	sfx := p5cSfx()
	silver, gold, _ := p5cTiers(t, sa, sfx)
	p5cEarnRule(t, sa, sfx)
	goldie := customer(t, sa, "P5RG"+sfx, "Gita Gold "+sfx, map[string]any{"email": "gita" + sfx + "@p5.test"})
	silvia := customer(t, sa, "P5RS"+sfx, "Silvia Silver "+sfx, map[string]any{"email": "silvia" + sfx + "@p5.test"})
	ga := str(sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": goldie, "tierId": gold}).JSON()["id"])
	sv := str(sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": silvia, "tierId": silver}).JSON()["id"])
	for _, a := range []string{ga, sv} {
		sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+a+":adjust", map[string]any{"points": 1000, "reason": "P5 reward test " + sfx})
	}

	// Reward with a cost and an eligibility rule: Gold and above, once a year.
	rw := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/rewards", map[string]any{"code": "P5W" + sfx, "name": "Golf towel " + sfx, "rewardType": "merchandise",
		"pointsCost": 100, "stock": 10, "unitCost": "75000"}))
	if r := sa.Do("POST", "/api/v1/crm/loyalty/reward-rules", map[string]any{"code": "P5X" + sfx, "name": "Bad", "validFrom": "2026-12-01", "validTo": "2026-01-01"}); r.Status != 422 {
		t.Fatalf("invalid period: %s", r)
	}
	rule := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/reward-rules", map[string]any{"code": "P5E" + sfx, "name": "Towel for Gold+", "rewardId": rw,
		"minTierId": gold, "maxPerCustomer": 1, "limitPeriod": "year"}))
	sa.Must(200, "PATCH", "/api/v1/crm/loyalty/reward-rules/"+rule, map[string]any{"lookbackDays": 180})
	if r := fd.Do("POST", "/api/v1/crm/loyalty/accounts/"+sv+":redeem-reward", map[string]any{"rewardId": rw}); r.Status != 409 ||
		!strings.Contains(string(r.Body), "not_eligible") {
		t.Fatalf("silver redeems a Gold reward: %s", r)
	}
	for _, x := range sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+sv+"/eligible-rewards", nil).Items() {
		if x["id"] == rw && (x["eligible"] != false || x["reason"] != "tier_required") {
			t.Fatalf("eligibility of silver: %v", x)
		}
	}
	fd.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+ga+":redeem-reward", map[string]any{"rewardId": rw})
	if r := fd.Do("POST", "/api/v1/crm/loyalty/accounts/"+ga+":redeem-reward", map[string]any{"rewardId": rw}); r.Status != 409 ||
		!strings.Contains(string(r.Body), "reward_limit_reached") {
		t.Fatalf("second towel within the year: %s", r)
	}
	for _, x := range sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+ga+"/eligible-rewards", nil).Items() {
		if x["id"] == rw && (x["eligible"] != false || x["reason"] != "limit_reached") {
			t.Fatalf("eligibility after the limit: %v", x)
		}
	}
	// A deleted (archived) rule no longer restricts.
	sa.Must(204, "DELETE", "/api/v1/crm/loyalty/reward-rules/"+rule, nil)
	fd.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+sv+":redeem-reward", map[string]any{"rewardId": rw})

	// Member App: eligible rewards.
	mc := roleUser(t, inst, "member")
	memberCustomer(t, sa)
	if h := mc.Must(200, "GET", "/api/v1/member/loyalty", nil).JSON(); h["enrolled"] != true {
		mc.Must(200, "POST", "/api/v1/member/loyalty:join", nil)
	}
	if l := mc.Must(200, "GET", "/api/v1/member/loyalty/eligible-rewards", nil).Items(); !containsID(l, rw) {
		t.Fatalf("member eligible rewards: %v", l)
	}

	// Staff issue without points (reason required), hand over, cancel (stock back).
	sa.Must(422, "POST", "/api/v1/crm/loyalty/reward-issues", map[string]any{"customerId": goldie, "rewardId": rw})
	i1 := sa.Must(201, "POST", "/api/v1/crm/loyalty/reward-issues", map[string]any{"customerId": goldie, "rewardId": rw, "note": "Course closure goodwill"}).JSON()
	if i1["source"] != "staff" || i1["totalCost"] != "75000" || i1["status"] != "issued" {
		t.Fatalf("staff issue: %v", i1)
	}
	fd.Must(200, "POST", "/api/v1/crm/loyalty/reward-issues/"+str(i1["id"])+":fulfil", map[string]any{"note": "Handed over"})
	i2 := sa.Must(201, "POST", "/api/v1/crm/loyalty/reward-issues", map[string]any{"customerId": silvia, "rewardId": rw, "note": "Birthday"}).JSON()
	fd.Must(422, "POST", "/api/v1/crm/loyalty/reward-issues/"+str(i2["id"])+":cancel", map[string]any{})
	fd.Must(200, "POST", "/api/v1/crm/loyalty/reward-issues/"+str(i2["id"])+":cancel", map[string]any{"reason": "Duplicate"})
	fd.Must(409, "POST", "/api/v1/crm/loyalty/reward-issues/"+str(i2["id"])+":fulfil", map[string]any{})
	if n := p5cCount(t, `SELECT stock FROM crm.loyalty_rewards WHERE id = $1`, mustUUID(rw)); n != 10-2-1 { // two redemptions, one issue kept
		t.Fatalf("stock %d", n)
	}
	if n := pcEvents(t, "crm.reward_issued", "issueId", str(i1["id"])); n != 1 {
		t.Fatalf("crm.reward_issued: %d", n)
	}

	// Top Spender programme: the top 2 of the month get a reward and an invitation, once per period.
	fixPolicy(t, sa, "crm.loyalty_budget", func(v map[string]any) { v["budgetPercent"] = "100" })
	p5cPay(t, sa, goldie, "Wedding tasting "+sfx, "40000000")
	seg := idOf(sa.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "P5INV" + sfx, "name": "Top spender invitations " + sfx, "segmentType": "static"}))
	sa.Must(422, "POST", "/api/v1/crm/loyalty/top-spender-programs", map[string]any{"code": "P5T0" + sfx, "name": "No action"})
	prog := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/top-spender-programs", map[string]any{"code": "P5T" + sfx, "name": "Top 2 " + sfx, "periodType": "month",
		"topN": 2, "rewardId": rw, "invitationSegmentId": seg, "invitationMessage": "Halo {{.name}}, Anda peringkat {{.rank}} periode {{.period}}."}))
	sa.Must(200, "PATCH", "/api/v1/crm/loyalty/top-spender-programs/"+prog, map[string]any{"name": "Top 2 spenders " + sfx})
	period := time.Now().In(clubLoc(inst)).Format("2006-01")
	sa.Must(422, "POST", "/api/v1/crm/loyalty/top-spender-programs/"+prog+":run", map[string]any{"period": "2026-13"})
	run := sa.Must(200, "POST", "/api/v1/crm/loyalty/top-spender-programs/"+prog+":run", map[string]any{"period": period}).JSON()
	if run["ranked"].(float64) != 2 || run["issued"].(float64) != 2 || run["invited"].(float64) != 2 || run["created"] != true {
		var lines, total int
		sysQueryRow(t, inst, `SELECT count(*)::int, coalesce(sum(net_amount), 0)::bigint FROM reporting.eng_folio_lines WHERE customer_id = $1`,
			[]any{mustUUID(goldie)}, &lines, &total)
		t.Fatalf("programme run: %v; ranking %s; goldie lines %d total %d; now %s", run,
			sa.Do("GET", "/api/v1/crm/top-spenders?period=month&limit=3", nil), lines, total, time.Now().In(clubLoc(inst)))
	}
	again := sa.Must(200, "POST", "/api/v1/crm/loyalty/top-spender-programs/"+prog+":run", map[string]any{"period": period}).JSON()
	if again["id"] != run["id"] || again["created"] != false {
		t.Fatalf("second run of the period: %v", again)
	}
	if n := p5cCount(t, `SELECT count(*) FROM crm.loyalty_reward_issues WHERE source = 'top_spender' AND source_id = $1`, mustUUID(prog)); n != 2 {
		t.Fatalf("issues of the programme: %d", n)
	}
	if n := p5cCount(t, `SELECT count(*) FROM crm.segment_members WHERE segment_id = $1`, mustUUID(seg)); n != 2 {
		t.Fatalf("invitation list: %d", n)
	}
	if l := sa.Must(200, "GET", "/api/v1/crm/loyalty/top-spender-program-runs?filter[programId]="+prog, nil).Items(); len(l) != 1 {
		t.Fatalf("runs: %v", l)
	}
	if l := sa.Must(200, "GET", "/api/v1/crm/loyalty/reward-issues?filter[source]=top_spender", nil).Items(); len(l) < 2 {
		t.Fatalf("reward issues: %v", l)
	}

	// Budget: a reward above the budget is skipped for automatic issues.
	big := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/rewards", map[string]any{"code": "P5BIG" + sfx, "name": "Car " + sfx, "pointsCost": 1,
		"unitCost": "900000000000"}))
	prog2 := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/top-spender-programs", map[string]any{"code": "P5U" + sfx, "name": "Over budget " + sfx,
		"topN": 1, "rewardId": big}))
	over := sa.Must(200, "POST", "/api/v1/crm/loyalty/top-spender-programs/"+prog2+":run", map[string]any{"period": period}).JSON()
	if over["skippedBudget"].(float64) != 1 || over["issued"].(float64) != 0 {
		t.Fatalf("budget not enforced: %v", over)
	}
	b := sa.Must(200, "GET", "/api/v1/crm/loyalty/budget?period="+period, nil).JSON()
	if !dec(b["netRevenue"]).IsPositive() || !dec(b["rewardCost"]).IsPositive() || b["budgetPercent"] != "100" || b["withinBudget"] != true {
		t.Fatalf("budget: %v", b)
	}
	sa.Must(422, "GET", "/api/v1/crm/loyalty/budget?period=10-2026", nil)
	sa.Must(409, "DELETE", "/api/v1/crm/loyalty/top-spender-programs/"+prog2, nil) // has runs
	sa.Must(200, "PATCH", "/api/v1/crm/loyalty/top-spender-programs/"+prog2, map[string]any{"status": "inactive"})
	sa.Must(409, "POST", "/api/v1/crm/loyalty/top-spender-programs/"+prog2+":run", map[string]any{"period": period})
	unused := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/top-spender-programs", map[string]any{"code": "P5D" + sfx, "name": "Unused " + sfx,
		"invitationSegmentId": seg}))
	sa.Must(204, "DELETE", "/api/v1/crm/loyalty/top-spender-programs/"+unused, nil)
	sa.Must(200, "PATCH", "/api/v1/crm/loyalty/top-spender-programs/"+prog, map[string]any{"status": "inactive"})
	mc.Must(200, "GET", "/api/v1/member/loyalty/issued-rewards", nil)
	if rows := engReport(t, sa, "crm.reward_redemption", "params[source]=top_spender"); len(rows) < 2 {
		t.Fatalf("Reward Redemption Report: %v", rows)
	}
	if rows := engReport(t, sa, "crm.loyalty_cost", "params[from]="+period+"-01"); len(rows) == 0 || !dec(rows[len(rows)-1]["totalCost"]).IsPositive() {
		t.Fatalf("Loyalty Programme Cost Report: %v", rows)
	}
}

func p5cEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s:\n got  %v\n want %v", what, got, want)
	}
}

// EP-19 AC: a member whose membership ends within 30 days and who has not
// renewed receives the messages in order; the journey stops automatically
// once the member pays the renewal. Also: journey template, activation
// through the approval engine, the anchored start (no H-60 offer at H-25),
// waits until H-7, goal conversion, crm.journey_step_executed, performance
// and the Journey Performance Report.
func TestP5CRMJourneyRenewal(t *testing.T) {
	sa := superAdmin(t, inst)
	mk := roleUser(t, inst, "marketing_staff")
	sfx := p5cSfx()
	p5cIsolate(t, sa)
	p5cQuietAway(t, sa, nil)
	prog := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "P5G" + sfx, "name": "P5 Golf " + sfx, "programKind": "golf"}))
	typ, pkg := membershipType(t, sa, prog, "P5I"+sfx, "P5 Individual "+sfx, map[string]any{"entitlements": map[string]any{"memberRate": true, "golf": true}})
	rina := customer(t, sa, "P5JR"+sfx, "Rina Renewal "+sfx, map[string]any{"email": "rina" + sfx + "@p5.test", "phone": "+62815" + sfx,
		"birthDate": dateAgo(40, 2, 0)})
	ms := activeMembership(t, sa, rina, typ, pkg, nil)
	ends := time.Now().In(clubLoc(inst)).AddDate(0, 0, 25)
	sysExec(t, inst, `UPDATE membership.memberships SET ends_on = $2 WHERE id = $1`, mustUUID(ms), ends.Format("2006-01-02"))
	p5cOptIn(t, sa, rina)

	j := mk.Must(201, "POST", "/api/v1/crm/journeys:from-template", map[string]any{"template": "renewal", "code": "JR" + sfx, "name": "Renewal " + sfx}).JSON()
	jid := str(j["id"])
	if j["status"] != "draft" || len(j["steps"].([]any)) != 10 || j["triggerDate"] != "membership_expiry" || j["exitEvents"].([]any)[0] != "membership.renewed" {
		t.Fatalf("renewal template: %v", j)
	}
	if l := mk.Must(200, "GET", "/api/v1/crm/journey-templates", nil).Items(); len(l) != 6 {
		t.Fatalf("templates: %v", l)
	}
	j = mk.Must(200, "POST", "/api/v1/crm/journeys/"+jid+":activate", nil).JSON()
	if j["status"] != "active" || j["approvalRequestId"] == nil || j["activatedAt"] == nil {
		t.Fatalf("activation: %v", j)
	}
	mk.Must(409, "POST", "/api/v1/crm/journeys/"+jid+":activate", nil)

	// H-25: the member joins at the H-30 stage (no H-60 offer) and waits for H-7.
	p5cRun(t, mk, jid)
	en := p5cEnrollment(t, mk, jid, rina)
	eid := str(en["id"])
	if en["anchorDate"] != ends.Format("2006-01-02") || en["status"] != "active" || en["currentKey"] != "check_h7" {
		t.Fatalf("enrollment: %v", en)
	}
	stage1 := []string{"check_h30:condition_false", "reminder_h30:sent", "wait_h7:waiting"}
	p5cEqual(t, "H-30 stage", p5cSteps(t, mk, jid, eid), stage1)
	for _, e := range mk.Must(200, "GET", "/api/v1/crm/journeys/"+jid+"/events?filter[enrollmentId]="+eid, nil).Items() {
		if e["stepKey"] == "reminder_h30" && (e["channel"] != "whatsapp" || !strings.Contains(str(e["body"]), ends.Format("02 Jan 2006")) ||
			!strings.Contains(str(e["body"]), "Rina Renewal")) {
			t.Fatalf("H-30 message: %v", e)
		}
	}
	if n := p5cCount(t, `SELECT count(*) FROM platform.notification_deliveries WHERE event_code = 'crm.journey_message' AND channel = 'whatsapp'
		AND payload->>'trackingToken' IN (SELECT token FROM crm.journey_events WHERE enrollment_id = $1)`, mustUUID(eid)); n != 1 {
		t.Fatalf("WhatsApp deliveries: %d", n)
	}
	// Running again before H-7 sends nothing more.
	p5cRun(t, mk, jid)
	p5cEqual(t, "still waiting", p5cSteps(t, mk, jid, eid), stage1)

	// H-7: the next reminder.
	p5cDue(t, eid)
	p5cRun(t, mk, jid)
	stage2 := append(append([]string{}, stage1...), "check_h7:condition_false", "reminder_h7:sent", "wait_h0:waiting")
	p5cEqual(t, "H-7 stage", p5cSteps(t, mk, jid, eid), stage2)

	// The member pays the renewal: the journey stops (exit rule) and converts (goal).
	ren := sa.Must(201, "POST", "/api/v1/membership/memberships/"+ms+":renew", map[string]any{"packageId": pkg}).JSON()
	if fid := ren["folioId"]; fid != nil {
		bal := sa.Must(200, "GET", "/api/v1/billing/folios/"+str(fid), nil).JSON()["summary"].(map[string]any)["balance"]
		if dec(bal).IsPositive() {
			sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": fid, "methodType": "cash", "amount": str(bal)})
		}
	}
	engDispatch(t, "renewal ends the journey", func() bool { return p5cEnrollment(t, mk, jid, rina)["status"] == "exited" })
	en = p5cEnrollment(t, mk, jid, rina)
	if en["exitReason"] != "event membership.renewed" || en["convertedAt"] == nil {
		t.Fatalf("exit after renewal: %v", en)
	}
	p5cDue(t, eid)
	p5cRun(t, mk, jid)
	p5cEqual(t, "no message after the renewal", p5cSteps(t, mk, jid, eid), append(stage2, "exit:exited"))
	if n := pcEvents(t, "crm.journey_step_executed", "enrollmentId", eid); n != len(stage2)+1 {
		t.Fatalf("crm.journey_step_executed: %d", n)
	}
	perf := mk.Must(200, "GET", "/api/v1/crm/journeys/"+jid+"/performance", nil).JSON()
	if perf["sent"].(float64) < 2 || perf["converted"].(float64) < 1 || perf["exited"].(float64) < 1 {
		t.Fatalf("performance: %v", perf)
	}
	for _, s := range perf["steps"].([]any) {
		st := s.(map[string]any)
		if st["key"] == "reminder_h30" && (st["sent"].(float64) < 1 || st["converted"].(float64) < 1) {
			t.Fatalf("step performance: %v", st)
		}
	}
	rows := engReport(t, mk, "crm.journey_performance", "")
	found := false
	for _, r := range rows {
		found = found || (r["journey"] == "JR"+sfx && r["step"] == "H-30 reminder" && r["sent"].(float64) >= 1)
	}
	if !found {
		t.Fatalf("Journey Performance Report: %v", rows)
	}
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+jid+":complete", map[string]any{"reason": "P5 test done"})
}

// FR-JRN-01/05/06/07: multi-step journey with A/B message, wait, branch,
// points, tag, voucher, reward and sales task; consent, suppression and the
// frequency cap (2 per week) at every message; quiet hours (21.00–08.00
// policy) defer marketing but not transactional messages; control group and
// experiments; Member App offers with read and click tracking; pause, edit,
// resume, manual exit, complete and archive.
func TestP5CRMJourneyRules(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := p5cSfx()
	p5cIsolate(t, sa)
	p5cQuietAway(t, sa, nil)
	fixPolicy(t, sa, "crm.loyalty_budget", func(v map[string]any) { v["budgetPercent"] = "100" })
	p5cEarnRule(t, sa, sfx)
	cust := func(code, name string, optIn bool) string {
		c := customer(t, sa, code+sfx, name+" "+sfx, map[string]any{"email": strings.ToLower(code) + sfx + "@p5.test", "phone": "+62817" + sfx[len(sfx)-4:] + code[len(code)-1:]})
		if optIn {
			p5cOptIn(t, sa, c)
		}
		return c
	}
	c1 := cust("P5J1", "Joko Paid", true)
	c2 := cust("P5J2", "Juli NoConsent", false)
	c3 := cust("P5J3", "Jaka Suppressed", true)
	c4 := cust("P5J4", "Jeni Capped", true)
	sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": c1})
	var phone3 string
	sysQueryRow(t, inst, `SELECT phone FROM crm.customers WHERE id = $1`, []any{mustUUID(c3)}, &phone3)
	sa.Must(201, "POST", "/api/v1/crm/suppressions", map[string]any{"channel": "whatsapp", "address": phone3, "customerId": c3, "reason": "Bounced"})

	// Validation of the definition.
	if r := sa.Do("POST", "/api/v1/crm/journeys", map[string]any{"code": "JX" + sfx, "name": "Bad", "triggerType": "event", "triggerEvent": "nope",
		"steps": []map[string]any{{"key": "m", "stepType": "message"}}}); r.Status != 422 || !strings.Contains(string(r.Body), "triggerEvent") {
		t.Fatalf("invalid journey: %s", r)
	}

	// Two messages this week bring c4 to the cap (2 per week).
	warm := p5cJourney(t, sa, map[string]any{"code": "JW" + sfx, "name": "Warm-up " + sfx, "triggerType": "manual", "steps": []map[string]any{
		p5cMsg("w1", "email", "Halo {{.name}} 1", nil), p5cMsg("w2", "email", "Halo {{.name}} 2", nil)}})
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+str(warm["id"])+"/enrollments", map[string]any{"customerIds": []string{c4}})
	p5cRun(t, sa, str(warm["id"]))
	p5cEqual(t, "warm-up", p5cSteps(t, sa, str(warm["id"]), str(p5cEnrollment(t, sa, str(warm["id"]), c4)["id"])), []string{"w1:sent", "w2:sent"})

	rw := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/rewards", map[string]any{"code": "P5JW" + sfx, "name": "Range bucket " + sfx, "pointsCost": 1,
		"unitCost": "20000"}))
	main := p5cJourney(t, sa, map[string]any{"code": "JM" + sfx, "name": "Main " + sfx, "triggerType": "manual", "goalEvent": "billing.payment_settled",
		"goalDays": 7, "steps": []map[string]any{
			p5cMsg("offer", "whatsapp", "Halo {{.name}}, promo {{.promoCode}}", map[string]any{"bodyB": "Hai {{.name}}, kode {{.promoCode}}", "splitPercent": 50,
				"offerTitle": "Weekend 2-for-1", "promoCode": "p5off", "offerValidDays": 14}),
			{"key": "wait", "stepType": "wait", "name": "3 days", "waitDays": 3},
			{"key": "paid", "stepType": "condition", "name": "Paid?", "conditionKind": "paid", "onFalse": "voucher"},
			{"key": "bonus", "stepType": "points", "name": "Bonus", "points": 50},
			{"key": "tag", "stepType": "tag", "name": "Tag", "tag": "p5_engaged", "nextKey": "end"},
			{"key": "voucher", "stepType": "voucher", "name": "F&B voucher", "voucherTypeRef": journey.DefaultVoucherType},
			{"key": "reward", "stepType": "reward", "name": "Reward", "rewardId": rw},
			{"key": "call", "stepType": "sales_task", "name": "Sales call", "taskSubject": "Call {{.name}}", "taskDueDays": 1},
		}})
	mid := str(main["id"])
	all := []string{c1, c2, c3, c4}
	if r := sa.Must(200, "POST", "/api/v1/crm/journeys/"+mid+"/enrollments", map[string]any{"customerIds": all}).JSON(); r["enrolled"].(float64) != 4 {
		t.Fatalf("enroll: %v", r)
	}
	if r := sa.Must(200, "POST", "/api/v1/crm/journeys/"+mid+"/enrollments", map[string]any{"customerIds": all}).JSON(); r["skipped"].(float64) != 4 {
		t.Fatalf("re-enroll the same occurrence: %v", r)
	}
	p5cRun(t, sa, mid)
	eids := map[string]string{}
	for i, c := range all {
		eids[c] = str(p5cEnrollment(t, sa, mid, c)["id"])
		want := []string{"offer:sent", "offer:skipped_no_consent", "offer:skipped_suppressed", "offer:skipped_frequency_cap"}[i]
		p5cEqual(t, "first step of "+c, p5cSteps(t, sa, mid, eids[c]), []string{want, "wait:waiting"})
	}
	// c1 pays: the goal converts and the branch takes the paid path.
	pay := p5cPay(t, sa, c1, "Brunch "+sfx, "300000")
	engDispatch(t, "conversion", func() bool { return p5cEnrollment(t, sa, mid, c1)["convertedAt"] != nil })
	if e := p5cEnrollment(t, sa, mid, c1); dec(e["revenue"]).IntPart() != 300000 {
		t.Fatalf("attributed revenue: %v", e)
	}
	for _, c := range all {
		p5cDue(t, eids[c])
	}
	p5cRun(t, sa, mid)
	p5cEqual(t, "paid path", p5cSteps(t, sa, mid, eids[c1]), []string{"offer:sent", "wait:waiting", "paid:condition_true", "bonus:issued", "tag:tagged"})
	p5cEqual(t, "not paid path", p5cSteps(t, sa, mid, eids[c2]), []string{"offer:skipped_no_consent", "wait:waiting", "paid:condition_false",
		"voucher:issued", "reward:issued", "call:created"})
	if e := p5cEnrollment(t, sa, mid, c1); e["status"] != "completed" {
		t.Fatalf("c1 completed: %v", e)
	}
	if n := p5cCount(t, `SELECT count(*) FROM crm.loyalty_ledger l JOIN crm.loyalty_accounts a ON a.id = l.account_id WHERE a.customer_id = $1
		AND l.source_type = 'crm.journey' AND l.points = 50`, mustUUID(c1)); n != 1 {
		t.Fatalf("bonus points: %d", n)
	}
	if n := p5cCount(t, `SELECT count(*) FROM crm.customer_tags WHERE customer_id = $1 AND tag = 'p5_engaged'`, mustUUID(c1)); n != 1 {
		t.Fatalf("tag: %d", n)
	}
	if n := p5cCount(t, `SELECT count(*) FROM commercial.vouchers WHERE customer_id = $1 AND source_type = 'crm.journey'`, mustUUID(c2)); n != 1 {
		t.Fatalf("journey voucher: %d", n)
	}
	if n := p5cCount(t, `SELECT count(*) FROM crm.loyalty_reward_issues WHERE customer_id = $1 AND source = 'journey'`, mustUUID(c2)); n != 1 {
		t.Fatalf("journey reward: %d", n)
	}
	if n := p5cCount(t, `SELECT count(*) FROM crm.sales_activities WHERE customer_id = $1 AND status = 'open' AND subject LIKE 'Call Juli%'`, mustUUID(c2)); n != 1 {
		t.Fatalf("sales task: %d", n)
	}
	_ = pay

	// Quiet hours defer marketing messages; transactional journeys are not deferred or capped.
	now := time.Now().In(clubLoc(inst))
	fixPolicy(t, sa, journey.PolicyCode, func(v map[string]any) {
		v["quietStart"], v["quietEnd"] = now.Add(-time.Hour).Format("15:04"), now.Add(time.Hour).Format("15:04")
	})
	quiet := p5cJourney(t, sa, map[string]any{"code": "JQ" + sfx, "name": "Quiet " + sfx, "triggerType": "manual", "steps": []map[string]any{
		p5cMsg("q", "email", "Halo {{.name}}", nil)}})
	trans := p5cJourney(t, sa, map[string]any{"code": "JT" + sfx, "name": "Statement " + sfx, "triggerType": "manual", "category": "transactional",
		"steps": []map[string]any{p5cMsg("t", "email", "Your statement is ready, {{.name}}", nil)}})
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+str(quiet["id"])+"/enrollments", map[string]any{"customerIds": []string{c1}})
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+str(trans["id"])+"/enrollments", map[string]any{"customerIds": []string{c1, c4}})
	if r := p5cRun(t, sa, str(quiet["id"])); r["deferred"].(float64) != 1 {
		t.Fatalf("quiet hours: %v", r)
	}
	qe := p5cEnrollment(t, sa, str(quiet["id"]), c1)
	if next, _ := time.Parse(time.RFC3339, str(qe["nextRunAt"])); !next.After(time.Now()) || len(p5cSteps(t, sa, str(quiet["id"]), str(qe["id"]))) != 0 {
		t.Fatalf("deferred enrollment: %v", qe)
	}
	p5cRun(t, sa, str(trans["id"]))
	for _, c := range []string{c1, c4} {
		p5cEqual(t, "transactional", p5cSteps(t, sa, str(trans["id"]), str(p5cEnrollment(t, sa, str(trans["id"]), c)["id"])), []string{"t:sent"})
	}
	p5cQuietAway(t, sa, nil)
	p5cDue(t, str(qe["id"]))
	p5cRun(t, sa, str(quiet["id"]))
	p5cEqual(t, "after the quiet hours", p5cSteps(t, sa, str(quiet["id"]), str(qe["id"])), []string{"q:sent"})

	// Control group (50%) and A/B variants: experiments.
	var group []string
	for i := 0; i < 16; i++ {
		group = append(group, customer(t, sa, fmt.Sprintf("P5K%02d%s", i, sfx), fmt.Sprintf("Kontrol %d %s", i, sfx), map[string]any{
			"email": fmt.Sprintf("k%d%s@p5.test", i, sfx)}))
		p5cOptIn(t, sa, group[i])
	}
	ctl := p5cJourney(t, sa, map[string]any{"code": "JC" + sfx, "name": "Control " + sfx, "triggerType": "manual", "controlPercent": 50,
		"goalEvent": "billing.payment_settled", "steps": []map[string]any{p5cMsg("m", "email", "A {{.name}}", map[string]any{"bodyB": "B {{.name}}", "splitPercent": 50})}})
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+str(ctl["id"])+"/enrollments", map[string]any{"customerIds": group})
	p5cRun(t, sa, str(ctl["id"]))
	control := p5cCount(t, `SELECT count(*) FROM crm.journey_events WHERE journey_id = $1 AND outcome = 'control'`, mustUUID(str(ctl["id"])))
	sent := p5cCount(t, `SELECT count(*) FROM crm.journey_events WHERE journey_id = $1 AND outcome = 'sent'`, mustUUID(str(ctl["id"])))
	if control == 0 || sent == 0 || control+sent != 16 {
		t.Fatalf("control %d, sent %d", control, sent)
	}
	kinds := map[string]bool{}
	for _, x := range sa.Must(200, "GET", "/api/v1/crm/experiments", nil).Items() {
		if x["journeyId"] == ctl["id"] {
			kinds[str(x["kind"])] = true
			arms := x["arms"].([]any)
			total := arms[0].(map[string]any)["size"].(float64) + arms[1].(map[string]any)["size"].(float64)
			if (x["kind"] == "control_group" && total != 16) || (x["kind"] == "ab_test" && int(total) != sent) {
				t.Fatalf("experiment arms: %v", x)
			}
		}
	}
	if !kinds["control_group"] || !kinds["ab_test"] {
		t.Fatalf("experiments: %v", kinds)
	}

	// Member App offers: in-app message with an offer, read and tracked link.
	mc := roleUser(t, inst, "member")
	me := memberCustomer(t, sa)
	p5cOptIn(t, sa, me)
	offer := p5cJourney(t, sa, map[string]any{"code": "JO" + sfx, "name": "Members night " + sfx, "triggerType": "manual", "category": "transactional",
		"steps": []map[string]any{p5cMsg("m", "in_app", "Exclusive members' night for {{.name}}", map[string]any{"offerTitle": "Members night " + sfx,
			"promoCode": "P5NIGHT", "offerValidDays": 7})}})
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+str(offer["id"])+"/enrollments", map[string]any{"customerIds": []string{me}})
	p5cRun(t, sa, str(offer["id"]))
	var oid string
	for _, o := range mc.Must(200, "GET", "/api/v1/member/journey-offers", nil).Items() {
		if o["title"] == "Members night "+sfx && o["promoCode"] == "P5NIGHT" {
			oid = str(o["id"])
		}
	}
	if oid == "" {
		t.Fatal("member offer missing")
	}
	if o := mc.Must(200, "POST", "/api/v1/member/journey-offers/"+oid+":read", nil).JSON(); o["readAt"] == nil {
		t.Fatalf("read: %v", o)
	}
	mc.Must(404, "POST", "/api/v1/member/journey-offers/"+eids[c1]+":read", nil)
	var tok string
	sysQueryRow(t, inst, `SELECT token FROM crm.journey_events WHERE id = $1`, []any{mustUUID(oid)}, &tok)
	noFollow := anon(t, inst)
	noFollow.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if r := noFollow.Do("GET", "/api/v1/public/journey-links/"+tok, nil); r.Status != 302 {
		t.Fatalf("tracked link: %s", r)
	}
	if n := p5cCount(t, `SELECT click_count FROM crm.journey_events WHERE id = $1`, mustUUID(oid)); n != 1 {
		t.Fatalf("click count %d", n)
	}
	anon(t, inst).Must(404, "GET", "/api/v1/public/journey-links/nope", nil)

	// Pause, edit, resume, exit an enrollment, complete, archive.
	oj := str(offer["id"])
	def := sa.Must(200, "GET", "/api/v1/crm/journeys/"+oj, nil).JSON()
	put := map[string]any{"code": def["code"], "name": "Members night (edited) " + sfx, "category": "transactional", "triggerType": "manual",
		"steps": def["steps"]}
	sa.Must(409, "PUT", "/api/v1/crm/journeys/"+oj, put)
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+oj+":pause", nil)
	if r := sa.Must(200, "PUT", "/api/v1/crm/journeys/"+oj, put).JSON(); r["name"] != "Members night (edited) "+sfx || r["status"] != "paused" {
		t.Fatalf("edit while paused: %v", r)
	}
	if r := p5cRun(t, sa, oj); r["processed"].(float64) != 0 {
		t.Fatalf("paused journey processed: %v", r)
	}
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+oj+":activate", nil)
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+oj+"/enrollments", map[string]any{"customerIds": []string{c3}, "occurrence": "manual-exit"})
	xe := p5cEnrollment(t, sa, oj, c3)
	sa.Must(422, "POST", "/api/v1/crm/journeys/"+oj+"/enrollments/"+str(xe["id"])+":exit", map[string]any{})
	if r := sa.Must(200, "POST", "/api/v1/crm/journeys/"+oj+"/enrollments/"+str(xe["id"])+":exit", map[string]any{"reason": "Asked to stop"}).JSON(); r["status"] != "exited" {
		t.Fatalf("manual exit: %v", r)
	}
	sa.Must(422, "POST", "/api/v1/crm/journeys/"+oj+":complete", map[string]any{})
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+oj+":complete", map[string]any{"reason": "Campaign over"})
	sa.Must(409, "POST", "/api/v1/crm/journeys/"+oj+":pause", nil)
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+oj+":archive", nil)
	sa.Must(404, "GET", "/api/v1/crm/journeys/"+oj, nil)
	for _, jx := range []map[string]any{warm, main, quiet, trans, ctl} {
		sa.Must(200, "POST", "/api/v1/crm/journeys/"+str(jx["id"])+":complete", map[string]any{"reason": "P5 test done"})
	}
	sa.Must(200, "POST", "/api/v1/crm/journeys:run", nil)
	roleUser(t, inst, "front_desk").Must(403, "GET", "/api/v1/crm/journeys", nil)
}

// FR-JRN-02..04 priority journeys from the templates (PRD P5 §16 #15):
// birthday (date trigger, offer by tier), welcome (membership.activated),
// win-back after 60 days without a visit with the F&B voucher branch
// (§9.4), follow-up after a banquet event (banquet.event_completed) and the
// cancelled-booking template.
func TestP5CRMJourneyTemplates(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := p5cSfx()
	p5cIsolate(t, sa)
	p5cQuietAway(t, sa, nil)
	start := func(template string) string {
		j := sa.Must(201, "POST", "/api/v1/crm/journeys:from-template", map[string]any{"template": template, "code": "JT" + strings.ToUpper(template[:3]) + sfx}).JSON()
		sa.Must(200, "POST", "/api/v1/crm/journeys/"+str(j["id"])+":activate", nil)
		t.Cleanup(func() {
			sa.Do("POST", "/api/v1/crm/journeys/"+str(j["id"])+":complete", map[string]any{"reason": "P5 test done"})
		})
		return str(j["id"])
	}
	// Birthday today: greeting with the offer of a customer without Gold / Platinum.
	bd := time.Now().In(clubLoc(inst)).AddDate(-35, 0, 0).Format("2006-01-02")
	bella := customer(t, sa, "P5BD"+sfx, "Bella Birthday "+sfx, map[string]any{"email": "bella" + sfx + "@p5.test", "phone": "+62818" + sfx, "birthDate": bd})
	p5cOptIn(t, sa, bella)
	bj := start("birthday")
	p5cRun(t, sa, bj)
	p5cEqual(t, "birthday", p5cSteps(t, sa, bj, str(p5cEnrollment(t, sa, bj, bella)["id"])), []string{"by_tier:condition_false", "greeting:sent"})

	// Welcome on membership.activated (contract payload of membership).
	wina := customer(t, sa, "P5WL"+sfx, "Wina Welcome "+sfx, map[string]any{"email": "wina" + sfx + "@p5.test"})
	p5cOptIn(t, sa, wina)
	wj := start("welcome")
	engPublish(t, "membership.activated", "membership.membership", map[string]any{"membershipId": uuid.NewString(), "memberId": uuid.NewString(),
		"memberNo": "P5" + sfx, "customerId": wina, "endsOn": time.Now().AddDate(1, 0, 0).Format("2006-01-02")})
	engDispatch(t, "welcome enrollment", func() bool {
		return len(sa.Must(200, "GET", "/api/v1/crm/journeys/"+wj+"/enrollments?filter[customerId]="+wina, nil).Items()) == 1
	})
	p5cRun(t, sa, wj)
	p5cEqual(t, "welcome", p5cSteps(t, sa, wj, str(p5cEnrollment(t, sa, wj, wina)["id"])), []string{"welcome:sent", "wait_3d:waiting"})

	// Win-back: last visit 60 days ago → offer, 7 days without a booking → F&B voucher (§9.4).
	wanda := customer(t, sa, "P5WB"+sfx, "Wanda Winback "+sfx, map[string]any{"email": "wanda" + sfx + "@p5.test", "phone": "+62819" + sfx})
	p5cOptIn(t, sa, wanda)
	folio := engFolio(t, sa, wanda, "Old visit "+sfx, "500000")
	sysExec(t, inst, `UPDATE billing.folio_lines SET business_date = $2::date - 60, posted_at = now() - interval '60 days' WHERE folio_id = $1`, mustUUID(folio), engToday())
	wbj := start("win_back")
	p5cRun(t, sa, wbj)
	we := p5cEnrollment(t, sa, wbj, wanda)
	p5cEqual(t, "win-back offer", p5cSteps(t, sa, wbj, str(we["id"])), []string{"offer:sent", "wait_7d:waiting"})
	p5cDue(t, str(we["id"]))
	p5cRun(t, sa, wbj)
	p5cEqual(t, "win-back voucher", p5cSteps(t, sa, wbj, str(we["id"])), []string{"offer:sent", "wait_7d:waiting", "booked:condition_false",
		"fnb_voucher:issued", "voucher_msg:sent"})

	// Post-event follow-up on banquet.event_completed (customer of the event).
	evc := customer(t, sa, "P5EV"+sfx, "Evi Event "+sfx, map[string]any{"email": "evi" + sfx + "@p5.test"})
	p5cOptIn(t, sa, evc)
	typ := bqType(t, sa, "P5E"+sfx, "social")
	day := time.Now().AddDate(0, 0, -2)
	ev := sa.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": "Arisan " + sfx, "eventTypeId": typ, "customerId": evc, "start": rfc(day),
		"end": rfc(day.Add(4 * time.Hour)), "expectedPax": 40}).JSON()
	pj := start("post_event")
	engPublish(t, "banquet.event_completed", "banquet.event", map[string]any{"eventId": ev["id"], "number": ev["number"], "completedAt": rfc(time.Now()),
		"businessDate": time.Now().Format("2006-01-02"), "finalPax": 40, "consumption": []any{}})
	engDispatch(t, "post-event enrollment", func() bool {
		return len(sa.Must(200, "GET", "/api/v1/crm/journeys/"+pj+"/enrollments?filter[customerId]="+evc, nil).Items()) == 1
	})
	pe := p5cEnrollment(t, sa, pj, evc)
	p5cRun(t, sa, pj)
	p5cDue(t, str(pe["id"]))
	p5cRun(t, sa, pj)
	p5cEqual(t, "post-event", p5cSteps(t, sa, pj, str(pe["id"])), []string{"wait_1d:waiting", "thanks:sent", "wait_14d:waiting"})

	// Cancelled-booking template: a valid draft that can be archived.
	ab := sa.Must(201, "POST", "/api/v1/crm/journeys:from-template", map[string]any{"template": "abandoned_booking", "code": "JTAB" + sfx}).JSON()
	if ab["triggerEvent"] != "golf.booking_cancelled" || ab["status"] != "draft" {
		t.Fatalf("abandoned booking template: %v", ab)
	}
	sa.Must(200, "POST", "/api/v1/crm/journeys/"+str(ab["id"])+":archive", nil)
	sa.Must(422, "POST", "/api/v1/crm/journeys:from-template", map[string]any{"template": "nope"})
	// The demo seeds the five priority journeys (Active / Paused).
	codes := map[string]string{}
	for _, j := range sa.Must(200, "GET", "/api/v1/crm/journeys?q=JRN-&limit=50", nil).Items() {
		codes[str(j["code"])] = str(j["status"])
	}
	for _, c := range []string{"JRN-RENEWAL", "JRN-BIRTHDAY", "JRN-WELCOME", "JRN-WINBACK", "JRN-POSTEVENT"} {
		if codes[c] == "" {
			t.Fatalf("demo journey %s missing: %v", c, codes)
		}
	}
}

// EP-17 / EP-20: analytics store refresh (behaviour per line, RFM scores and
// groups, CLV), RFM movement between snapshots, segment tags from analytics
// and segment comparison, VIP (Top Spender, tier, manual) with the check-in /
// POS marker and Customer 360 section; NPS (drivers, retention), complaint
// SLA, sales & commission and CLV / cohort analytics; CRM reports.
func TestP5CRMAnalytics(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := p5cSfx()
	fixPolicy(t, sa, "crm.segmentation", func(v map[string]any) { v["vipTopSpenderRank"], v["vvipTopSpenderRank"] = 500, 0 })
	champ := customer(t, sa, "P5AC"+sfx, "Cakra Champion "+sfx, map[string]any{"email": "cakra" + sfx + "@p5.test"})
	lapsed := customer(t, sa, "P5AL"+sfx, "Lala Lapsed "+sfx, map[string]any{"email": "lala" + sfx + "@p5.test"})
	// Cross-business activity of the champion: golf, F&B and stay on six days of this week.
	for i, line := range []string{"golf", "pos", "stay", "golf", "pos", "stay"} {
		f := engFolio(t, sa, champ, fmt.Sprintf("Visit %d %s", i, sfx), "60000000")
		sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": f, "amount": "60000000", "methodType": "cash", "channel": "venue"})
		sysExec(t, inst, `UPDATE billing.folio_lines SET business_line = $2, business_date = $4::date - $3::int WHERE folio_id = $1`, mustUUID(f), line, i, engToday())
	}
	lf := engFolio(t, sa, lapsed, "Long ago "+sfx, "200000")
	sysExec(t, inst, `UPDATE billing.folio_lines SET business_date = $2::date - 250, posted_at = now() - interval '250 days' WHERE folio_id = $1`, mustUUID(lf), engToday())
	// A previous snapshot to compare with.
	ref := sa.Must(200, "POST", "/api/v1/crm/analytics:refresh", nil).JSON()
	if ref["customers"].(float64) < 2 {
		t.Fatalf("refresh: %v", ref)
	}
	// The club's date (as_of), not the database server's.
	sysExec(t, inst, `UPDATE crm.rfm_scores SET as_of = as_of - 30 WHERE as_of = $2::date AND customer_id = ANY($1::uuid[])`,
		[]uuid.UUID{mustUUID(champ), mustUUID(lapsed)}, engToday())
	sysExec(t, inst, `UPDATE crm.rfm_scores SET rfm_group = 'potential' WHERE as_of = $2::date - 30 AND customer_id = $1`, mustUUID(champ), engToday())
	sa.Must(200, "POST", "/api/v1/crm/analytics:refresh", nil)

	b := sa.Must(200, "GET", "/api/v1/crm/analytics/customers/"+champ, nil).JSON()
	lines := map[string]bool{}
	for _, l := range b["lines"].([]any) {
		lines[str(l.(map[string]any)["businessLine"])] = true
	}
	rfm := b["rfm"].(map[string]any)
	if !lines["golf"] || !lines["pos"] || !lines["stay"] || rfm["group"] != "champions" || rfm["rScore"].(float64) != 5 || len(rfm["lines"].([]any)) != 3 ||
		!dec(rfm["clv"]).IsPositive() || len(b["history"].([]any)) != 2 {
		t.Fatalf("behaviour of the champion: %v", b)
	}
	if l := sa.Must(200, "GET", "/api/v1/crm/analytics/customers/"+lapsed, nil).JSON()["rfm"].(map[string]any); l["group"] != "lapsed" {
		t.Fatalf("lapsed: %v", l)
	}
	sum := sa.Must(200, "GET", "/api/v1/crm/analytics/rfm", nil).JSON()
	if len(sum["groups"].([]any)) != 6 || sum["multiLine"].(float64) < 1 || sum["asOf"] == nil {
		t.Fatalf("RFM summary: %v", sum)
	}
	if l := sa.Must(200, "GET", "/api/v1/crm/analytics/rfm/customers?filter[group]=champions&order=clv", nil).Items(); !p5cHas(l, "customerId", champ) {
		t.Fatalf("champions: %v", l)
	}
	mv := sa.Must(200, "GET", "/api/v1/crm/analytics/rfm/movement", nil).JSON()
	moved := false
	for _, m := range mv["moves"].([]any) {
		x := m.(map[string]any)
		moved = moved || (x["fromGroup"] == "potential" && x["toGroup"] == "champions" && x["customers"].(float64) >= 1)
	}
	if !moved || mv["from"] == nil {
		t.Fatalf("movement: %v", mv)
	}
	sa.Must(422, "GET", "/api/v1/crm/analytics/rfm/movement?from=yesterday", nil)
	clv := sa.Must(200, "GET", "/api/v1/crm/analytics/clv", nil).JSON()
	if !dec(clv["totalClv"]).IsPositive() || len(clv["top"].([]any)) == 0 || clv["cohorts"] == nil {
		t.Fatalf("CLV: %v", clv)
	}

	// Segments use analytics tags; segment comparison.
	seg := idOf(sa.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "P5CH" + sfx, "name": "Champions " + sfx,
		"rules": map[string]any{"tags": []string{"rfm_champions"}}}))
	sa.Must(200, "POST", "/api/v1/crm/segments/"+seg+":refresh", nil)
	if n := p5cCount(t, `SELECT count(*) FROM crm.segment_members WHERE segment_id = $1 AND customer_id = $2`, mustUUID(seg), mustUUID(champ)); n != 1 {
		t.Fatal("rfm_champions tag in a segment")
	}
	cmp := sa.Must(200, "GET", "/api/v1/crm/analytics/segments?segmentIds="+seg, nil).Items()
	if len(cmp) != 1 || cmp[0]["members"].(float64) < 1 || cmp[0]["groups"].(map[string]any)["champions"] == nil {
		t.Fatalf("segment comparison: %v", cmp)
	}
	sa.Must(422, "GET", "/api/v1/crm/analytics/segments", nil)

	// VIP: the champion is a top spender; manual VIP; check-in / POS marker.
	fd := roleUser(t, inst, "front_desk")
	if f := fd.Must(200, "GET", "/api/v1/crm/vip-flags/"+champ, nil).JSON(); f["vip"] != true || f["source"] != "top_spender" || f["handlingNote"] == nil {
		t.Fatalf("VIP marker of the top spender: %v", f)
	}
	nobody := customer(t, sa, "P5AN"+sfx, "Nina Newcomer "+sfx, nil)
	if f := fd.Must(200, "GET", "/api/v1/crm/vip-flags/"+nobody, nil).JSON(); f["vip"] != false {
		t.Fatalf("not a VIP: %v", f)
	}
	sa.Must(422, "POST", "/api/v1/crm/vip", map[string]any{"customerId": lapsed})
	v := sa.Must(201, "POST", "/api/v1/crm/vip", map[string]any{"customerId": lapsed, "level": "vvip", "reason": "Owner's guest",
		"handlingNote": "Escort to the lounge"}).JSON()
	if v["source"] != "manual" || v["level"] != "vvip" {
		t.Fatalf("manual VIP: %v", v)
	}
	if l := sa.Must(200, "GET", "/api/v1/crm/vip?filter[source]=manual", nil).Items(); !containsID(l, str(v["id"])) {
		t.Fatalf("VIP list: %v", l)
	}
	c360 := sa.Must(200, "GET", "/api/v1/crm/customers/"+lapsed+"/360", nil).JSON()
	if sec, _ := c360["sections"].(map[string]any)["analytics"].(map[string]any); sec == nil || sec["vip"].(map[string]any)["level"] != "vvip" {
		t.Fatalf("Customer 360 analytics: %v", c360["sections"])
	}
	sa.Must(200, "POST", "/api/v1/crm/vip/"+str(v["id"])+":deactivate", map[string]any{"reason": "Guest list ended"})
	sa.Must(409, "POST", "/api/v1/crm/vip/"+str(v["id"])+":deactivate", map[string]any{"reason": "again"})
	fd.Must(403, "POST", "/api/v1/crm/vip", map[string]any{"customerId": lapsed, "reason": "x"})

	// NPS analytics: drivers from comments and retention.
	sa.Must(201, "POST", "/api/v1/crm/nps-responses", map[string]any{"customerId": champ, "score": 10, "comment": "Fairway and greens perfect", "businessLine": "golf"})
	sa.Must(201, "POST", "/api/v1/crm/nps-responses", map[string]any{"customerId": lapsed, "score": 3, "comment": "Pace was too slow, waited at every tee", "businessLine": "golf"})
	nps := sa.Must(200, "GET", "/api/v1/crm/analytics/nps?businessLine=golf", nil).JSON()
	drivers := map[string]map[string]any{}
	for _, d := range nps["drivers"].([]any) {
		drivers[str(d.(map[string]any)["driver"])] = d.(map[string]any)
	}
	if drivers["course_condition"] == nil || drivers["pace_of_play"] == nil || drivers["pace_of_play"]["detractors"].(float64) < 1 ||
		len(nps["retention"].([]any)) != 3 || len(nps["trend"].([]any)) == 0 {
		t.Fatalf("NPS analytics: %v", nps)
	}
	sla := sa.Must(200, "GET", "/api/v1/crm/analytics/sla", nil).JSON()
	if sla["byLine"] == nil || sla["recurringCategories"] == nil || sla["resolutionCompliance"] == nil {
		t.Fatalf("SLA analytics: %v", sla)
	}
	sales := sa.Must(200, "GET", "/api/v1/crm/analytics/sales", nil).JSON()
	if sales["funnel"] == nil || sales["forecast"] == nil || sales["commissionByScheme"] == nil || sales["winRate"] == nil {
		t.Fatalf("sales analytics: %v", sales)
	}
	var opps int
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.sales_opportunities WHERE property_id = $1 AND created_at >= now() - interval '180 days'`, []any{inst.Main}, &opps)
	if int(sales["opportunities"].(float64)) != opps {
		t.Fatalf("sales analytics opportunities %v ≠ %d", sales["opportunities"], opps)
	}
	sa.Must(422, "GET", "/api/v1/crm/analytics/sales?from=2026-10-05&to=2026-01-01", nil)
	roleUser(t, inst, "sales_executive").Must(200, "GET", "/api/v1/crm/analytics/sales", nil)
	roleUser(t, inst, "cashier").Must(403, "GET", "/api/v1/crm/analytics/sales", nil)

	// CRM reports of P5 (FR-RPT-P5-03).
	for _, code := range []string{"crm.nps_analytics", "crm.complaint_sla", "crm.sales_performance", "crm.rfm_segment", "crm.vip_customers"} {
		sa.Must(200, "GET", "/api/v1/reporting/reports/"+code, nil)
	}
	if rows := engReport(t, sa, "crm.rfm_segment", ""); len(rows) == 0 {
		t.Fatal("RFM Segment Report")
	}
	if rows := engReport(t, sa, "crm.vip_customers", "params[status]=active"); !p5cHas(rows, "customerCode", "P5AC"+sfx) {
		t.Fatalf("VIP Customer Report: %v", rows)
	}
	dash := sa.Must(200, "GET", "/api/v1/reporting/dashboards/crm-performance", nil).JSON()
	keys := map[string]bool{}
	for _, k := range dash["kpis"].([]any) {
		keys[str(k.(map[string]any)["key"])] = true
	}
	if !keys["journey_conversion"] || !keys["loyalty_cost_ratio"] || !keys["vip_customers"] {
		t.Fatalf("CRM Performance KPIs: %v", keys)
	}
	_ = context.Background
}

func p5cHas(items []map[string]any, key, value string) bool {
	for _, x := range items {
		if str(x[key]) == value {
			return true
		}
	}
	return false
}
