package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Member tier classes & classification (PRD P5 EP-18 §16 #14, product
// owner request "on member add tier class and with classification and
// CRUD"): the tier master (badge, qualification rules, evaluation settings,
// ladder validation, delete-or-archive), versioned thresholds with the
// "re-evaluate now" preview, manual classification overrides (approval,
// source, history, expiry), the tier badge on members / customers, the
// Members by Tier and Tier Movement reports, the tier F&B discount at the
// POS (exact amounts, stacking with promotions, offline cache) and the demo.

func init() {
	resourceCRUDSkip["crm.loyalty_tier"] = "ladder validation (unique rank, thresholds increasing): covered by TestP5TiersMaster"
}

// p5tIsolateTiers deactivates the active tiers of the main property for the
// test (the tier ladder is validated against the active tiers) and puts them
// back at the end; tiers created by the test must be deactivated by a
// cleanup registered after this one.
func p5tIsolateTiers(t *testing.T) {
	t.Helper()
	var ids []uuid.UUID
	sysQueryRow(t, inst, `SELECT coalesce(array_agg(id), '{}') FROM crm.loyalty_tiers WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL`,
		[]any{inst.Main}, &ids)
	sysExec(t, inst, `UPDATE crm.loyalty_tiers SET status = 'inactive' WHERE id = ANY($1)`, ids)
	t.Cleanup(func() {
		sysExec(t, inst, `UPDATE crm.loyalty_tiers SET status = 'active' WHERE id = ANY($1)`, ids)
	})
}

type p5tSet struct{ silver, gold, plat string }

// p5tTiers isolates the tiers and creates Silver / Gold / Platinum of PRD P5
// §16 #14 with their badges (deactivated at the end).
func p5tTiers(t *testing.T, sa *Client, sfx string) p5tSet {
	t.Helper()
	p5tIsolateTiers(t)
	mk := func(code, name string, rank int, spend, mult, disc, color, icon string, window int, vip bool) string {
		return idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/tiers", map[string]any{"code": code + sfx, "name": name + " " + sfx, "rank": rank,
			"minSpend": spend, "multiplier": mult, "fnbDiscountPercent": disc, "bookingWindowDays": window, "eventAccess": vip, "priorityService": vip,
			"color": color, "icon": icon, "benefits": name + " perks"}))
	}
	s := p5tSet{silver: mk("TS", "Silver", 1, "0", "1", "0", "#9CA3AF", "military_tech", 0, false)}
	s.gold = mk("TG", "Gold", 2, "25000000", "1.25", "5", "#C9A227", "star", 2, false)
	s.plat = mk("TP", "Platinum", 3, "75000000", "1.5", "10", "#4B5563", "diamond", 4, true)
	t.Cleanup(func() {
		sysExec(t, inst, `UPDATE crm.loyalty_tiers SET status = 'inactive' WHERE id = ANY($1::uuid[])`, []string{s.silver, s.gold, s.plat})
	})
	return s
}

// p5tAccount enrols a customer in a tier.
func p5tAccount(t *testing.T, sa *Client, cust, tier string) string {
	t.Helper()
	return str(sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": cust, "tierId": tier}).JSON()["id"])
}

func p5tAcct(t *testing.T, sa *Client, aid string) map[string]any {
	t.Helper()
	return sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+aid, nil).JSON()
}

// Tier master CRUD with the ladder validation, delete-or-archive, versioned
// thresholds (no silent re-tiering) and re-evaluate now with its preview,
// evaluation settings per tier, reports.
func TestP5TiersMaster(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := p5cSfx()
	ts := p5tTiers(t, sa, sfx)
	p5cEarnRule(t, sa, sfx)

	// Validation: rank 1..n unique, thresholds increasing with the rank, badge.
	bad := func(body map[string]any, code string) {
		t.Helper()
		base := map[string]any{"code": "TX" + sfx, "name": "Bad " + sfx}
		for k, v := range body {
			base[k] = v
		}
		if r := sa.Do("POST", "/api/v1/crm/loyalty/tiers", base); r.Status != 422 || !strings.Contains(string(r.Body), code) {
			t.Fatalf("%v: %s", body, r)
		}
	}
	bad(map[string]any{"rank": 2, "minSpend": "50000000"}, "taken")
	bad(map[string]any{"rank": 4, "minSpend": "60000000"}, "thresholds_not_increasing")
	bad(map[string]any{"rank": 0}, "too_small")
	bad(map[string]any{"rank": 4, "minSpend": "90000000", "color": "gold"}, "invalid_format")
	if r := sa.Do("PATCH", "/api/v1/crm/loyalty/tiers/"+ts.gold, map[string]any{"minSpend": "80000000"}); r.Status != 422 {
		t.Fatalf("Gold above Platinum: %s", r)
	}
	gold := sa.Must(200, "GET", "/api/v1/crm/loyalty/tiers/"+ts.gold, nil).JSON()
	if gold["color"] != "#C9A227" || gold["icon"] != "star" || gold["qualifyMode"] != "all" || gold["downgradeAllowed"] != true ||
		gold["thresholdVersion"].(float64) != 1 || gold["pendingThresholds"] != false {
		t.Fatalf("tier class: %v", gold)
	}
	classes := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-classes?filter[status]=active", nil).Items()
	if len(classes) != 3 || classes[2]["id"] != ts.plat || classes[2]["color"] != "#4B5563" || classes[1]["minSpend"] != "25000000" {
		t.Fatalf("tier classes: %v", classes)
	}

	// Delete only an unused tier; a tier an account has been in is archived.
	unused := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/tiers", map[string]any{"code": "TD" + sfx, "name": "Diamond " + sfx, "rank": 4,
		"minSpend": "150000000", "color": "#1D4ED8", "icon": "diamond"}))
	if r := sa.Must(200, "DELETE", "/api/v1/crm/loyalty/tiers/"+unused, nil).JSON(); r["result"] != "deleted" {
		t.Fatalf("unused tier: %v", r)
	}
	sa.Must(404, "GET", "/api/v1/crm/loyalty/tiers/"+unused, nil)
	old := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/tiers", map[string]any{"code": "TO" + sfx, "name": "Legacy " + sfx, "rank": 5,
		"minSpend": "200000000"}))
	lc := customer(t, sa, "P5TL"+sfx, "Lukas Legacy "+sfx, nil)
	la := p5tAccount(t, sa, lc, old)
	sa.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+la+":set-tier", map[string]any{"tierId": ts.silver, "reason": "Legacy tier retired"})
	if r := sa.Must(200, "DELETE", "/api/v1/crm/loyalty/tiers/"+old, nil).JSON(); r["result"] != "archived" {
		t.Fatalf("tier with history: %v", r)
	}
	var archived bool
	sysQueryRow(t, inst, `SELECT archived_at IS NOT NULL FROM crm.loyalty_tiers WHERE id = $1`, []any{mustUUID(old)}, &archived)
	if !archived {
		t.Fatal("tier with history must be archived")
	}

	// Versioned thresholds: Citra spends Rp20 jt in Silver; lowering Gold to
	// Rp15 jt waits for the next evaluation (no silent upgrade on earning).
	citra := customer(t, sa, "P5TC"+sfx, "Citra Versi "+sfx, map[string]any{"email": "citra" + sfx + "@p5t.test"})
	ca := p5tAccount(t, sa, citra, ts.silver)
	p5cPay(t, sa, citra, "Wedding anniversary "+sfx, "20000000")
	engDispatch(t, "points", func() bool { return engBalance(t, sa, ca) == 2000 })
	g2 := sa.Must(200, "PATCH", "/api/v1/crm/loyalty/tiers/"+ts.gold, map[string]any{"minSpend": "15000000", "graceMonths": 1}).JSON()
	if g2["thresholdVersion"].(float64) != 2 || g2["effectiveVersion"].(float64) != 1 || g2["pendingThresholds"] != true {
		t.Fatalf("pending threshold version: %v", g2)
	}
	vs := sa.Must(200, "GET", "/api/v1/crm/loyalty/tiers/"+ts.gold+"/versions", nil).Items()
	if len(vs) != 2 || vs[0]["status"] != "pending" || vs[0]["minSpend"] != "15000000" || vs[1]["status"] != "effective" || vs[1]["minSpend"] != "25000000" {
		t.Fatalf("versions: %v", vs)
	}
	p5cPay(t, sa, citra, "Lunch "+sfx, "1000000")
	engDispatch(t, "points", func() bool { return engBalance(t, sa, ca) == 2100 })
	if a := p5tAcct(t, sa, ca); str(a["tierId"]) != ts.silver {
		t.Fatalf("thresholds changed silently re-tiered: %v", a["tierId"])
	}
	// Preview of re-evaluate now: in force vs. pending thresholds.
	moved := func(p map[string]any) bool {
		for _, l := range p["lines"].([]any) {
			if l.(map[string]any)["accountId"] == ca {
				return l.(map[string]any)["outcome"] == "upgraded" && l.(map[string]any)["toTierName"] == "Gold "+sfx
			}
		}
		return false
	}
	if p := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-evaluations:preview?kind=periodic&pending=false", nil).JSON(); moved(p) {
		t.Fatalf("preview with the thresholds in force: %v", p["lines"])
	}
	p := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-evaluations:preview?kind=periodic", nil).JSON()
	if !moved(p) || p["upgraded"].(float64) < 1 || !strings.Contains(str(p["pendingTiers"]), "TG"+sfx) {
		t.Fatalf("preview with the pending thresholds: %v", p)
	}
	up := false
	for _, m := range p["moves"].([]any) {
		mm := m.(map[string]any)
		up = up || (mm["fromTierName"] == "Silver "+sfx && mm["toTierName"] == "Gold "+sfx && mm["direction"] == "up" && mm["accounts"].(float64) >= 1)
	}
	if !up {
		t.Fatalf("preview moves: %v", p["moves"])
	}
	if a := p5tAcct(t, sa, ca); str(a["tierId"]) != ts.silver {
		t.Fatal("a preview changes nothing")
	}
	run := sa.Must(201, "POST", "/api/v1/crm/loyalty/tier-evaluations", map[string]any{"kind": "periodic", "accountIds": []string{ca},
		"applyPendingVersions": true}).JSON()
	av := run["appliedVersions"].([]any)
	if run["upgraded"].(float64) != 1 || len(av) != 1 || av[0].(map[string]any)["tierId"] != ts.gold || av[0].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("re-evaluate now: %v", run)
	}
	if a := p5tAcct(t, sa, ca); str(a["tierId"]) != ts.gold {
		t.Fatalf("re-evaluated: %v", a["tierId"])
	}
	vs = sa.Must(200, "GET", "/api/v1/crm/loyalty/tiers/"+ts.gold+"/versions", nil).Items()
	if vs[0]["status"] != "effective" || vs[0]["evaluationId"] != run["id"] || vs[1]["status"] != "superseded" {
		t.Fatalf("versions after the run: %v", vs)
	}
	if g := sa.Must(200, "GET", "/api/v1/crm/loyalty/tiers/"+ts.gold, nil).JSON(); g["effectiveVersion"].(float64) != 2 || g["pendingThresholds"] != false {
		t.Fatalf("gold in force: %v", g)
	}

	// Evaluation settings per tier: Platinum is never downgraded, Gold has a
	// 1-month grace (programme: 3 months).
	sa.Must(200, "PATCH", "/api/v1/crm/loyalty/tiers/"+ts.plat, map[string]any{"downgradeAllowed": false})
	dodi := customer(t, sa, "P5TD"+sfx, "Dodi Tetap "+sfx, nil)
	da := p5tAccount(t, sa, dodi, ts.plat)
	eka := customer(t, sa, "P5TE"+sfx, "Eka Tenggang "+sfx, nil)
	ea := p5tAccount(t, sa, eka, ts.gold)
	ann := sa.Must(201, "POST", "/api/v1/crm/loyalty/tier-evaluations", map[string]any{"kind": "annual", "accountIds": []string{da, ea}}).JSON()
	det := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-evaluations/"+str(ann["id"]), nil).JSON()
	for _, l := range det["lines"].([]any) {
		line := l.(map[string]any)
		switch line["accountId"] {
		case da:
			if line["outcome"] != "retained" || str(line["toTierId"]) != ts.plat {
				t.Fatalf("downgrade not allowed: %v", line)
			}
		case ea:
			if line["outcome"] != "grace_started" || line["graceUntil"] != time.Now().In(clubLoc(inst)).AddDate(0, 1, 0).Format("2006-01-02") {
				t.Fatalf("grace of the tier: %v", line)
			}
		}
	}
	// Members by Tier and Tier Movement reports.
	found := false
	for _, r := range engReport(t, sa, "crm.members_by_tier", "params[tierCode]=TG"+sfx) {
		found = found || (r["tier"] == "Gold "+sfx && r["members"].(float64) == 2 && r["color"] == "#C9A227" && r["movedUp"].(float64) == 1 &&
			dec(r["spend"]).IntPart() == 21000000 && r["inGrace"].(float64) == 1)
	}
	if !found {
		t.Fatalf("Members by Tier: %v", engReport(t, sa, "crm.members_by_tier", "params[tierCode]=TG"+sfx))
	}
	found = false
	for _, r := range engReport(t, sa, "crm.tier_movement", "params[movement]=evaluation") {
		found = found || (r["number"] == run["number"] && r["upgraded"].(float64) == 1 && r["versionsApplied"].(float64) == 1)
	}
	if !found {
		t.Fatal("Tier Movement report")
	}
	// A full periodic run (P3 Evaluate Tiers) keeps both.
	sa.Must(200, "POST", "/api/v1/crm/loyalty/tiers:evaluate", nil)
	if str(p5tAcct(t, sa, da)["tierId"]) != ts.plat || str(p5tAcct(t, sa, ea)["tierId"]) != ts.gold {
		t.Fatal("periodic run")
	}
}

// Manual classification override: permission crm.loyalty.tier_override,
// approval workflow for big jumps, source and history, locked at the
// evaluation, Set Tier refused, revoke and expiry back to the tier before;
// the tier badge on members / customers and in the Member App.
func TestP5TiersOverride(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := p5cSfx()
	ts := p5tTiers(t, sa, sfx)
	mm := roleUser(t, inst, "membership_manager")
	fd := roleUser(t, inst, "front_desk")
	gm := roleUser(t, inst, "general_manager")
	fajar := customer(t, sa, "P5TF"+sfx, "Fajar Dewan "+sfx, map[string]any{"email": "fajar" + sfx + "@p5t.test"})
	fa := p5tAccount(t, sa, fajar, ts.silver)

	fd.Must(403, "POST", "/api/v1/crm/loyalty/accounts/"+fa+":override-tier", map[string]any{"tierId": ts.gold, "reason": "x"})
	mm.Must(422, "POST", "/api/v1/crm/loyalty/accounts/"+fa+":override-tier", map[string]any{"tierId": ts.gold})
	mm.Must(422, "POST", "/api/v1/crm/loyalty/accounts/"+fa+":override-tier", map[string]any{"tierId": ts.gold, "reason": "x", "validUntil": "2020-01-01"})
	until := time.Now().In(clubLoc(inst)).AddDate(0, 0, 30).Format("2006-01-02")
	o := mm.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+fa+":override-tier", map[string]any{"tierId": ts.gold, "reason": "Board member",
		"validUntil": until}).JSON()
	if o["status"] != "active" || o["tierName"] != "Gold "+sfx || o["previousTierName"] != "Silver "+sfx || o["validUntil"] != until || o["approvalRequestId"] == nil {
		t.Fatalf("override without a workflow: %v", o)
	}
	if a := p5tAcct(t, sa, fa); str(a["tierId"]) != ts.gold || a["tierLocked"] != true {
		t.Fatalf("overridden account: %v", a)
	}
	b := fd.Must(200, "GET", "/api/v1/crm/loyalty/tier-benefits?customerId="+fajar, nil).JSON()
	if b["tierSource"] != "manual" || b["overridden"] != true || b["overrideUntil"] != until || b["tierColor"] != "#C9A227" || b["tierIcon"] != "star" {
		t.Fatalf("benefits with the source: %v", b)
	}
	h := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+fa+"/tier-history", nil).Items()
	if len(h) == 0 || h[0]["source"] != "manual" || h[0]["overrideNumber"] != o["number"] || h[0]["direction"] != "up" || h[0]["toTierName"] != "Gold "+sfx {
		t.Fatalf("tier history: %v", h)
	}
	if n := pcEvents(t, "crm.tier_changed", "overrideId", str(o["id"])); n != 1 {
		t.Fatalf("crm.tier_changed of the override: %d", n)
	}
	engDispatch(t, "tier notification", func() bool {
		return p5cCount(t, `SELECT count(*) FROM platform.notification_deliveries WHERE event_code = 'crm.loyalty_tier_changed' AND recipient = $1`,
			"fajar"+sfx+"@p5t.test") > 0
	})
	ev := sa.Must(201, "POST", "/api/v1/crm/loyalty/tier-evaluations", map[string]any{"kind": "annual", "accountIds": []string{fa}}).JSON()
	if ev["retained"].(float64) != 1 || str(p5tAcct(t, sa, fa)["tierId"]) != ts.gold {
		t.Fatalf("locked at the evaluation: %v", ev)
	}
	sa.Must(409, "POST", "/api/v1/crm/loyalty/accounts/"+fa+":set-tier", map[string]any{"tierId": ts.plat, "reason": "x"})
	// Badge lookups for lists and pickers.
	ct := fd.Must(200, "GET", "/api/v1/crm/loyalty/customer-tiers?customerIds="+fajar+","+uuid.NewString(), nil).Items()
	if len(ct) != 1 || ct[0]["tierName"] != "Gold "+sfx || ct[0]["tierSource"] != "manual" {
		t.Fatalf("customer tiers: %v", ct)
	}
	fd.Must(422, "GET", "/api/v1/crm/loyalty/customer-tiers?customerIds=x", nil)
	if l := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-overrides?filter[status]=active&filter[accountId]="+fa, nil).Items(); len(l) != 1 {
		t.Fatalf("active overrides: %v", l)
	}

	// Revoke: back to the tier held before, automatic again.
	r := mm.Must(200, "POST", "/api/v1/crm/loyalty/tier-overrides/"+str(o["id"])+":revoke", map[string]any{"reason": "Term ended"}).JSON()
	if r["status"] != "revoked" || str(p5tAcct(t, sa, fa)["tierId"]) != ts.silver || p5tAcct(t, sa, fa)["tierLocked"] != false {
		t.Fatalf("revoked: %v", r)
	}
	if b := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-benefits?customerId="+fajar, nil).JSON(); b["tierSource"] != "auto" || b["overridden"] != false {
		t.Fatalf("after the revoke: %v", b)
	}
	mm.Must(409, "POST", "/api/v1/crm/loyalty/tier-overrides/"+str(o["id"])+":revoke", map[string]any{"reason": "again"})

	// A workflow for big jumps (two ranks or more): pending until the GM approves.
	wf := idOf(sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "loyalty_tier_override", "name": "Tier jumps " + sfx,
		"steps": []map[string]any{{"stepNo": 1, "name": "General Manager", "approverType": "role", "approverRoleId": roleID(t, sa, "general_manager"),
			"conditions": []map[string]any{{"attribute": "rankChange", "operator": "gte", "value": 2}}}}}))
	t.Cleanup(func() {
		sa.Do("PATCH", "/api/v1/platform/approval-workflows/"+wf, map[string]any{"status": "inactive"})
	})
	big := mm.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+fa+":override-tier", map[string]any{"tierId": ts.plat, "reason": "Founder"}).JSON()
	if big["status"] != "pending" || str(p5tAcct(t, sa, fa)["tierId"]) != ts.silver {
		t.Fatalf("pending override: %v", big)
	}
	mm.Must(409, "POST", "/api/v1/crm/loyalty/accounts/"+fa+":override-tier", map[string]any{"tierId": ts.gold, "reason": "twice"})
	gm.Must(200, "POST", "/api/v1/platform/approvals/"+str(big["approvalRequestId"])+":approve", map[string]any{"reason": "OK"})
	if o := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-overrides?filter[accountId]="+fa+"&filter[status]=active", nil).Items(); len(o) != 1 ||
		o[0]["id"] != big["id"] || str(p5tAcct(t, sa, fa)["tierId"]) != ts.plat {
		t.Fatalf("approved override: %v", o)
	}
	// Expiry: the day after valid until the member returns to Silver.
	sysExec(t, inst, `UPDATE crm.loyalty_tier_overrides SET valid_until = $2::date - 1 WHERE id = $1`, mustUUID(str(big["id"])), engToday())
	if err := inst.App.Engage.Loyalty.RunTierOverridesDaily(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := p5tAcct(t, sa, fa); str(a["tierId"]) != ts.silver || a["tierLocked"] != false {
		t.Fatalf("expired override: %v", a)
	}
	if o := sa.Must(200, "GET", "/api/v1/crm/loyalty/tier-overrides?filter[accountId]="+fa, nil).Items(); o[0]["status"] != "ended" || o[0]["endReason"] != "valid until passed" {
		t.Fatalf("ended override: %v", o[0])
	}
	found := false
	for _, r := range engReport(t, sa, "crm.tier_movement", "params[movement]=override") {
		found = found || (r["number"] == big["number"] && r["upgraded"].(float64) == 1)
	}
	if !found {
		t.Fatal("Tier Movement report: override")
	}

	// Members list with the tier class (demo members DT001…, PRD §16 #14 tiers).
	dm := fd.Must(200, "GET", "/api/v1/crm/loyalty/member-tiers?filter[memberNo]=dt001", nil).Items()
	// (the full evaluation runs of other tests may have re-tiered the demo members)
	if len(dm) != 1 || dm[0]["memberName"] != "Ratna Wijaya" || dm[0]["tierId"] == nil || dm[0]["tierColor"] == nil || dm[0]["status"] != "active" {
		t.Fatalf("member tier: %v", dm)
	}
	if b := fd.Must(200, "GET", "/api/v1/crm/loyalty/tier-benefits?customerId="+str(dm[0]["customerId"]), nil).JSON(); b["tierId"] != dm[0]["tierId"] {
		t.Fatalf("member tier = customer tier: %v %v", b, dm[0])
	}
	byTier := fd.Must(200, "GET", "/api/v1/crm/loyalty/member-tiers?limit=200&filter[tierId]="+str(dm[0]["tierId"]), nil).Items()
	if !containsID(byTier, str(dm[0]["id"])) {
		t.Fatalf("members by tier filter: %v", byTier)
	}
	for _, m := range byTier {
		if m["tierId"] != dm[0]["tierId"] {
			t.Fatalf("filter by tier: %v", m)
		}
	}
	for _, m := range fd.Must(200, "GET", "/api/v1/crm/loyalty/member-tiers?limit=200&filter[tierId]=none", nil).Items() {
		if m["tierId"] != nil {
			t.Fatalf("not classified: %v", m)
		}
	}
	ov := fd.Must(200, "GET", "/api/v1/crm/loyalty/member-tiers?q=Yusuf%20Hakim", nil).Items()
	if len(ov) != 1 || ov[0]["tierCode"] != "GOLD" || ov[0]["tierSource"] != "manual" || ov[0]["overrideUntil"] == nil {
		t.Fatalf("demo manual classification: %v", ov)
	}
	fd.Must(422, "GET", "/api/v1/crm/loyalty/member-tiers?filter[tierId]=gold", nil)

	// Member App: tier badge and progress.
	mc := roleUser(t, inst, "member")
	me := memberCustomer(t, sa)
	if h := mc.Must(200, "GET", "/api/v1/member/loyalty", nil).JSON(); h["enrolled"] != true {
		mc.Must(200, "POST", "/api/v1/member/loyalty:join", nil)
	}
	var macct string
	sysQueryRow(t, inst, `SELECT id::text FROM crm.loyalty_accounts WHERE customer_id = $1`, []any{mustUUID(me)}, &macct)
	before := p5tAcct(t, sa, macct)
	sa.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+macct+":set-tier", map[string]any{"tierId": ts.gold, "reason": "P5 tier badge test"})
	t.Cleanup(func() {
		sa.Do("POST", "/api/v1/crm/loyalty/accounts/"+macct+":set-tier", map[string]any{"tierId": before["tierId"], "lock": before["tierLocked"],
			"reason": "restore after the P5 tier badge test"})
	})
	pr := mc.Must(200, "GET", "/api/v1/member/loyalty/progress", nil).JSON()
	pb := pr["benefits"].(map[string]any)
	if pb["tierName"] != "Gold "+sfx || pb["tierColor"] != "#C9A227" || pb["tierIcon"] != "star" || pr["nextTier"].(map[string]any)["id"] != ts.plat {
		t.Fatalf("member app badge: %v", pr)
	}
}

// Tier F&B discount at the POS: a separate "Gold member 5%" line on the F&B
// items with exact amounts, not on retail; no stacking (the better of the
// promotion and the tier discount), stacking within the Promotion Policies
// ceiling; the benefit cached for offline terminals and the offline total.
func TestP5TiersPOSDiscount(t *testing.T) {
	sa := superAdmin(t, inst)
	cashier := roleUser(t, inst, "cashier")
	sfx := p5cSfx()
	ts := p5tTiers(t, sa, sfx)
	gita := customer(t, sa, "P5TG"+sfx, "Gita Gold "+sfx, nil)
	ga := p5tAccount(t, sa, gita, ts.gold)
	_ = ga
	walk := customer(t, sa, "P5TW"+sfx, "Wawan Walk-in "+sfx, nil)

	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "TPO" + sfx, "name": "Tier Lounge " + sfx, "outletType": "restaurant",
		"pricingMode": "nett"}))
	product := func(code, typ, price string) string {
		return idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": code + sfx, "name": code + " " + sfx, "productType": typ,
			"price": price}))
	}
	nasi := product("TPNASI", "food", "150000")
	teh := product("TPTEH", "beverage", "33333")
	balls := product("TPBALL", "retail", "200000")
	shift := str(sa.Must(201, "POST", "/api/v1/commercial/shifts:open", map[string]any{"outletId": outlet, "openingCash": "0"}).JSON()["id"])
	// Other tests' promotions stay out of the sale.
	var others []string
	for _, p := range sa.Must(200, "GET", "/api/v1/commercial/promotions?filter[status]=active&limit=500", nil).Items() {
		others = append(others, str(p["id"]))
	}
	order := func(cust string, extra map[string]any) map[string]any {
		t.Helper()
		body := map[string]any{"outletId": outlet, "shiftId": shift, "lines": []map[string]any{{"productId": nasi}, {"productId": teh}, {"productId": balls}}}
		if cust != "" {
			body["customerId"] = cust
		}
		if len(others) > 0 {
			body["promotionExclusions"] = others
		}
		for k, v := range extra {
			body[k] = v
		}
		return sa.Must(201, "POST", "/api/v1/commercial/orders", body, "Idempotency-Key", newKey()).JSON()
	}
	tierOf := func(o map[string]any, product string) string { return str(pcLine(o, product)["tierDiscount"]) }

	// The benefit as the POS caches it with the member (offline).
	td := cashier.Must(200, "GET", "/api/v1/commercial/pos/tier-discount?customerId="+gita, nil).JSON()
	if td["enabled"] != true || td["percent"] != "5" || td["label"] != "Gold "+sfx+" member 5%" || td["stackWithPromotions"] != false ||
		len(td["productTypes"].([]any)) != 2 {
		t.Fatalf("tier discount for the POS: %v", td)
	}
	if w := cashier.Must(200, "GET", "/api/v1/commercial/pos/tier-discount?customerId="+walk, nil).JSON(); w["enabled"] != false {
		t.Fatalf("customer without a tier: %v", w)
	}

	// Gold member 5% on F&B: Rp150.000 → 7.500, Rp33.333 → 1.667, retail none.
	o := order(gita, nil)
	if o["tierName"] != "Gold "+sfx || o["tierDiscountPercent"] != "5" || o["tierDiscountLabel"] != "Gold "+sfx+" member 5%" || o["tierDiscount"] != "9167" {
		t.Fatalf("tier discount line: %v", o)
	}
	if tierOf(o, nasi) != "7500" || tierOf(o, teh) != "1667" || tierOf(o, balls) != "0" || o["total"] != "374166" {
		t.Fatalf("tier discount per line: %v %v %v total %v", tierOf(o, nasi), tierOf(o, teh), tierOf(o, balls), o["total"])
	}
	if n := order(walk, nil); n["tierDiscount"] != "0" || n["total"] != "383333" || n["tierName"] != nil {
		t.Fatalf("no tier: %v", n)
	}
	receipt := string(sa.Must(200, "GET", "/api/v1/commercial/orders/"+str(o["id"])+"/receipt", nil).Body)
	if !strings.Contains(receipt, "Gold "+sfx+" member 5%") || !strings.Contains(receipt, "9.167") {
		t.Fatalf("receipt: %s", receipt)
	}
	// Paid: the discount stays.
	sa.Must(200, "POST", "/api/v1/commercial/orders/"+str(o["id"])+":pay", map[string]any{"shiftId": shift, "tenders": []map[string]any{{"methodType": "cash"}}},
		"Idempotency-Key", newKey())

	// A 10% promotion on the food: not stacked, the better one (promotion
	// Rp15.000 > tier Rp7.500); the beverage keeps the tier discount.
	promo := idOf(sa.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "TPP" + sfx, "name": "Food 10% " + sfx,
		"promoType": "percent_discount", "discountPercent": "10", "productIds": []string{nasi}, "channels": []string{"pos"}}))
	if st := sa.Must(200, "POST", "/api/v1/commercial/promotions/"+promo+":activate", nil).JSON(); st["status"] != "active" {
		t.Fatalf("promotion: %v", st)
	}
	t.Cleanup(func() {
		sa.Do("POST", "/api/v1/commercial/promotions/"+promo+":deactivate", map[string]any{"reason": "end of the P5 tier test"})
	})
	o = order(gita, nil)
	if str(pcLine(o, nasi)["promotionDiscount"]) != "15000" || tierOf(o, nasi) != "0" || tierOf(o, teh) != "1667" || o["total"] != "366666" {
		t.Fatalf("not stacked: %v %v %v", pcLine(o, nasi), tierOf(o, teh), o["total"])
	}
	// Stacked: 5% of the rest (Rp135.000) = 6.750; within a 12% ceiling 3.000.
	fixPolicy(t, sa, "pos.tier_discount", func(v map[string]any) { v["stackWithPromotions"] = true })
	if o = order(gita, nil); tierOf(o, nasi) != "6750" || o["total"] != "359916" {
		t.Fatalf("stacked: %v total %v", tierOf(o, nasi), o["total"])
	}
	fixPolicy(t, sa, "commercial.promotion", func(v map[string]any) { v["maxStackedPercent"] = "12" })
	if o = order(gita, nil); tierOf(o, nasi) != "3000" || o["tierDiscount"] != "4667" {
		t.Fatalf("stacking ceiling: %v %v", tierOf(o, nasi), o["tierDiscount"])
	}

	// Offline sale priced with the cached benefit: same total, no review;
	// a different total is flagged.
	at := time.Now().UTC().Format(time.RFC3339)
	if off := order(gita, map[string]any{"offline": true, "clientTotal": o["total"], "clientCreatedAt": at}); off["promotionMismatch"] != false ||
		off["tierDiscount"] != o["tierDiscount"] {
		t.Fatalf("offline order with the cached benefit: %v", off)
	}
	if off := order(gita, map[string]any{"offline": true, "clientTotal": "383333", "clientCreatedAt": at}); off["promotionMismatch"] != true {
		t.Fatalf("offline total without the tier discount: %v", off)
	}
	// Disabled in the Pricing Policies: no tier discount on new orders.
	fixPolicy(t, sa, "pos.tier_discount", func(v map[string]any) { v["enabled"] = false })
	if o = order(gita, nil); o["tierDiscount"] != "0" || o["tierName"] != nil {
		t.Fatalf("disabled: %v", o)
	}
}
