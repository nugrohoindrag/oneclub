package e2e

import (
	"context"
	"testing"
	"time"
)

func customer(t *testing.T, c *Client, code, name string, extra map[string]any) string {
	t.Helper()
	b := map[string]any{"code": code, "name": name}
	user := ""
	for k, v := range extra {
		if k == "userId" {
			user = str(v) // the portal login link is set by P1's activation, not the API
			continue
		}
		b[k] = v
	}
	cid := idOf(c.Must(201, "POST", "/api/v1/crm/customers", b))
	if user != "" {
		sysExec(t, inst, `UPDATE crm.customers SET user_id = $2 WHERE id = $1`, mustUUID(cid), mustUUID(user))
	}
	return cid
}

func dateAgo(years, months, days int) string {
	return time.Now().AddDate(-years, -months, -days).Format("2006-01-02")
}

// EP-04 acceptance: family eligibility, multi-program membership, pause
// extends validity, unpaid annual fee suspends automatically, lifecycle.
func TestP2MembershipLifecycle(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	golf := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "P2-GOLF", "name": "Golf Membership", "programKind": "golf"}))
	sport := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "SPORT", "name": "Sport Club Membership", "programKind": "sport_club"}))
	famRes, famResPkg := membershipType(t, sa, sport, "SC-FAM-RES", "Family Residence", map[string]any{"category": "family",
		"joiningFee": "5000000", "annualFee": "3000000", "graceDays": 30, "rank": 2, "maxFamilyMembers": 4,
		"eligibility":  map[string]any{"residentOnly": true, "maxChildAge": 21, "maxChildren": 3},
		"entitlements": map[string]any{"memberRate": true, "freeEntry": true, "memberCharge": true, "guestQuotaPerMonth": 4}})
	golfInd, golfIndPkg := membershipType(t, sa, golf, "GOLF-IND", "Golf Individual", map[string]any{"annualFee": "10000000", "graceDays": 30, "rank": 1,
		"entitlements": map[string]any{"memberRate": true, "golf": true, "memberCharge": true}})
	golfCorp, _ := membershipType(t, sa, golf, "GOLF-PREMIER", "Golf Premier", map[string]any{"annualFee": "16000000", "graceDays": 30, "rank": 5,
		"cardReplacementFee": "150000", "reactivationFee": "1000000", "entitlements": map[string]any{"memberRate": true, "golf": true, "memberCharge": true}})

	parent := customer(t, sa, "MB-PARENT", "Budi Santoso", map[string]any{"birthDate": dateAgo(45, 0, 0), "resident": true, "gender": "male", "email": "budi@p2.test"})
	spouse := customer(t, sa, "MB-SPOUSE", "Ani Santoso", map[string]any{"birthDate": dateAgo(42, 0, 0), "gender": "female"})
	child22 := customer(t, sa, "MB-CHILD22", "Raka Santoso", map[string]any{"birthDate": dateAgo(22, 0, 1)})
	child10 := customer(t, sa, "MB-CHILD10", "Tia Santoso", map[string]any{"birthDate": dateAgo(10, 0, 0)})
	family := func(child string) []map[string]any {
		return []map[string]any{{"customerId": spouse, "relationship": "spouse"}, {"customerId": child, "relationship": "child"}}
	}

	// AC: a family application with a 22-year-old child fails the eligibility check.
	bad := idOf(sa.Must(201, "POST", "/api/v1/membership/applications", map[string]any{"typeId": famRes, "packageId": famResPkg, "customerId": parent,
		"dependents": family(child22)}))
	el := sa.Must(200, "POST", "/api/v1/membership/applications/"+bad+":check-eligibility", nil).JSON()["eligibility"].(map[string]any)
	if el["eligible"] != false {
		t.Fatalf("22-year-old child must fail: %v", el)
	}
	if r := sa.Do("POST", "/api/v1/membership/applications/"+bad+":submit", nil); r.Status != 422 {
		t.Fatalf("ineligible submit: %s", r)
	}
	// P1 application flow; P2 sets the first annual fee due a year after the start.
	msID := activeMembership(t, sa, parent, famRes, famResPkg, family(child10))
	if _, err := inst.App.Dispatcher.DispatchPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	ms := sa.Must(200, "GET", "/api/v1/membership/memberships/"+msID, nil).JSON()
	if ms["status"] != "active" || ms["typeCode"] != "SC-FAM-RES" || ms["nextFeeDue"] == nil {
		t.Fatalf("membership: %v", ms)
	}
	dependents := 0
	for _, m := range sa.Must(200, "GET", "/api/v1/membership/memberships?limit=200", nil).Items() {
		if str(m["principalId"]) == msID {
			dependents++
		}
	}
	if dependents != 2 {
		t.Fatalf("spouse and child follow the principal: %d", dependents)
	}
	// Multi-program: the same member also holds Golf; one card covers both.
	gid := activeMembership(t, sa, parent, golfInd, golfIndPkg, nil)
	sysExec(t, inst, `UPDATE membership.memberships SET starts_on = $2 WHERE id = $1`, mustUUID(gid), dateAgo(0, 8, 0)) // pause needs 6 months
	ent := sa.Must(200, "GET", "/api/v1/membership/entitlements?customerId="+parent, nil).JSON()
	if len(ent["memberships"].([]any)) != 2 {
		t.Fatalf("multi-program: %v", ent)
	}
	card := sa.Must(201, "POST", "/api/v1/membership/cards", map[string]any{"memberId": ms["memberId"], "cardType": "digital"}).JSON()
	look := sa.Must(200, "GET", "/api/v1/membership/cards:lookup?code="+str(card["cardNumber"]), nil).JSON()
	if len(look["memberships"].([]any)) != 2 {
		t.Fatalf("card lookup: %v", look)
	}
	cards := []map[string]any{card}
	// AC: pause 3 months extends validity 3 months and disables Member Rate.
	endBefore, _ := time.Parse("2006-01-02", str(sa.Must(200, "GET", "/api/v1/membership/memberships/"+gid, nil).JSON()["endsOn"])[:10])
	from := time.Now().Format("2006-01-02")
	until := time.Now().AddDate(0, 3, 0).Format("2006-01-02")
	pr := sa.Must(202, "POST", "/api/v1/membership/memberships/"+gid+":pause", map[string]any{"from": from, "until": until, "reason": "overseas assignment"}).JSON()
	if pr["status"] != "applied" && pr["status"] != "approved" {
		t.Fatalf("pause request: %v", pr)
	}
	paused := sa.Must(200, "GET", "/api/v1/membership/memberships/"+gid, nil).JSON()
	endAfter, _ := time.Parse("2006-01-02", str(paused["endsOn"])[:10])
	if paused["status"] != "paused" || endAfter.Sub(endBefore).Hours()/24 < 89 {
		t.Fatalf("pause: status %v end %v → %v", paused["status"], endBefore, endAfter)
	}
	ent = sa.Must(200, "GET", "/api/v1/membership/entitlements?customerId="+parent, nil).JSON()
	for _, m := range ent["memberships"].([]any) {
		mm := m.(map[string]any)
		if mm["programKind"] == "golf" && mm["status"] != "paused" {
			t.Fatalf("golf must be paused: %v", mm)
		}
	}
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+gid+":resume", map[string]any{"reason": "back early"})

	// Upgrade with prorated fee, card replacement, members.
	prev := sa.Must(200, "GET", "/api/v1/membership/memberships/"+gid+"/change-preview?typeId="+golfCorp, nil).JSON()
	if prev["direction"] != "upgrade" || prev["difference"] == "0" {
		t.Fatalf("preview: %v", prev)
	}
	sa.Must(202, "POST", "/api/v1/membership/memberships/"+gid+":upgrade", map[string]any{"typeId": golfCorp, "reason": "member request"})
	if tc := sa.Must(200, "GET", "/api/v1/membership/memberships/"+gid, nil).JSON()["typeCode"]; tc != "GOLF-PREMIER" {
		t.Fatalf("upgrade applied: %v", tc)
	}
	if r := sa.Do("POST", "/api/v1/membership/memberships/"+gid+":downgrade", map[string]any{"typeId": golfCorp}); r.Status != 422 {
		t.Fatalf("same type: %s", r)
	}
	sa.Must(202, "POST", "/api/v1/membership/memberships/"+gid+":downgrade", map[string]any{"typeId": golfInd, "reason": "cost"})
	nc := sa.Must(201, "POST", "/api/v1/membership/cards/"+str(cards[0]["id"])+":replace", map[string]any{"reason": "lost"}).JSON()
	if nc["status"] != "active" {
		t.Fatalf("new card: %v", nc)
	}
	for _, c := range sa.Must(200, "GET", "/api/v1/membership/cards?filter[memberId]="+str(ms["memberId"]), nil).Items() {
		if c["id"] == cards[0]["id"] && c["status"] != "replaced" {
			t.Fatalf("old card replaced: %v", c)
		}
	}
	child2 := customer(t, sa, "MB-CHILD2", "Dio Santoso", map[string]any{"birthDate": dateAgo(15, 0, 0)})
	covered := sa.Must(201, "POST", "/api/v1/membership/memberships/"+msID+"/members", map[string]any{"customerId": child2, "relationship": "child"}).JSON()
	if str(covered["principalId"]) != msID || covered["role"] != "family" {
		t.Fatalf("covered child: %v", covered)
	}
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+msID+"/members/"+str(covered["id"])+":remove", map[string]any{"reason": "moved abroad"})

	// AC: annual fee unpaid after the grace period → Suspended, member rate and member charge refused.
	late := customer(t, sa, "MB-LATE", "Late Payer", map[string]any{"email": "late@p2.test"})
	lid := activeMembership(t, sa, late, golfInd, golfIndPkg, nil)
	// the annual fee fell due 40 days ago (grace 30 days) and is unpaid
	sysExec(t, inst, `UPDATE membership.memberships SET starts_on = $2, next_fee_due = $3 WHERE id = $1`, mustUUID(lid), dateAgo(1, 1, 10), dateAgo(0, 0, 40))
	sum, err := inst.App.Membership.RunDaily(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Suspended < 1 {
		t.Fatalf("daily job: %+v", sum)
	}
	ls := sa.Must(200, "GET", "/api/v1/membership/memberships/"+lid, nil).JSON()
	if ls["status"] != "suspended" || ls["suspensionKind"] != "arrears" {
		t.Fatalf("auto suspension: %v", ls["status"])
	}
	acc := sa.Must(200, "GET", "/api/v1/billing/customer-accounts?filter[customerId]="+late, nil).Items()
	if len(acc) != 1 || acc[0]["status"] != "suspended" {
		t.Fatalf("member account suspended: %v", acc)
	}
	fees := sa.Must(200, "GET", "/api/v1/membership/annual-fees?filter[membershipId]="+lid+"&filter[status]=due", nil).Items()
	if len(fees) != 1 {
		t.Fatalf("due fee: %v", fees)
	}
	sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": str(fees[0]["folioId"]), "methodType": "cash", "amount": fees[0]["amount"]})
	waitFor(t, 20*time.Second, "lift after payment", func() bool {
		return sa.Must(200, "GET", "/api/v1/membership/memberships/"+lid, nil).JSON()["status"] == "active"
	})
	renewed := sa.Must(200, "GET", "/api/v1/membership/memberships/"+lid, nil).JSON()
	if end, _ := time.Parse("2006-01-02", str(renewed["endsOn"])[:10]); !end.After(time.Now()) {
		t.Fatalf("paid annual fee must extend validity: %v", renewed["endsOn"])
	}
	// Discipline suspension → lift; suspend → reactivation (approval, fee); postpone; renew; waive; cancel.
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+lid+":suspend", map[string]any{"reason": "conduct"})
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+lid+":lift-suspension", map[string]any{"reason": "appeal accepted"})
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+lid+":suspend", map[string]any{"reason": "conduct again"})
	sa.Must(202, "POST", "/api/v1/membership/memberships/"+lid+":reactivate", map[string]any{"reason": "board decision"})
	if st := sa.Must(200, "GET", "/api/v1/membership/memberships/"+lid, nil).JSON()["status"]; st != "active" {
		t.Fatalf("reactivated: %v", st)
	}
	// The next annual fee enters the H-30 window: the daily job schedules it; postpone, renew, waive.
	sysExec(t, inst, `UPDATE membership.memberships SET next_fee_due = $2 WHERE id = $1`, mustUUID(lid), time.Now().AddDate(0, 0, 20).Format("2006-01-02"))
	if _, err := inst.App.Membership.RunDaily(context.Background()); err != nil {
		t.Fatal(err)
	}
	nd := time.Now().AddDate(0, 3, 0).Format("2006-01-02")
	sa.Must(202, "POST", "/api/v1/membership/memberships/"+lid+":postpone", map[string]any{"dueDate": nd, "reason": "hardship"})
	sa.Must(201, "POST", "/api/v1/membership/memberships/"+lid+":renew", nil)
	var open map[string]any
	for _, fe := range sa.Must(200, "GET", "/api/v1/membership/annual-fees?filter[membershipId]="+lid, nil).Items() {
		if fe["status"] == "scheduled" || fe["status"] == "postponed" || fe["status"] == "due" {
			open = fe
		}
	}
	if open == nil {
		t.Fatal("scheduled annual fee")
	}
	sa.Must(200, "POST", "/api/v1/membership/annual-fees/"+str(open["id"])+":waive", map[string]any{"reason": "goodwill"})
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+lid+":cancel", map[string]any{"reason": "member resigned"})
	if n := len(sa.Must(200, "GET", "/api/v1/membership/requests", nil).Items()); n < 4 {
		t.Fatalf("requests: %d", n)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/membership/memberships?filter[programKind]=golf", nil).Items()); n < 2 {
		t.Fatalf("list memberships %d", n)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/membership/applications?filter[status]=completed", nil).Items()); n < 3 {
		t.Fatalf("applications %d", n)
	}
}
