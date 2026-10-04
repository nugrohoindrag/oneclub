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
	famRes := idOf(sa.Must(201, "POST", "/api/v1/membership/types", map[string]any{"code": "SC-FAM-RES", "name": "Family Residence", "programId": sport,
		"category": "family", "joiningFee": "5000000", "annualFee": "3000000", "graceDays": 30, "rank": 2, "maxMembers": 5,
		"eligibility":  map[string]any{"residentRequired": true, "family": map[string]any{"maxAdults": 2, "maxChildren": 3, "maxChildAge": 21, "childrenUnmarried": true}},
		"entitlements": map[string]any{"memberRate": true, "freeEntry": true, "memberCharge": true, "guestQuotaPerMonth": 4}}))
	golfInd := idOf(sa.Must(201, "POST", "/api/v1/membership/types", map[string]any{"code": "GOLF-IND", "name": "Golf Individual", "programId": golf,
		"category": "individual", "annualFee": "10000000", "graceDays": 30, "rank": 1,
		"entitlements": map[string]any{"memberRate": true, "golf": true, "memberCharge": true}}))
	golfCorp := idOf(sa.Must(201, "POST", "/api/v1/membership/types", map[string]any{"code": "GOLF-PREMIER", "name": "Golf Premier", "programId": golf,
		"category": "individual", "annualFee": "16000000", "graceDays": 30, "rank": 5, "cardReplacementFee": "150000", "reactivationFee": "1000000",
		"entitlements": map[string]any{"memberRate": true, "golf": true, "memberCharge": true}}))

	parent := customer(t, sa, "MB-PARENT", "Budi Santoso", map[string]any{"birthDate": dateAgo(45, 0, 0), "resident": true, "gender": "male", "email": "budi@p2.test"})
	spouse := customer(t, sa, "MB-SPOUSE", "Ani Santoso", map[string]any{"birthDate": dateAgo(42, 0, 0), "gender": "female"})
	child22 := customer(t, sa, "MB-CHILD22", "Raka Santoso", map[string]any{"birthDate": dateAgo(22, 0, 1)})
	child10 := customer(t, sa, "MB-CHILD10", "Tia Santoso", map[string]any{"birthDate": dateAgo(10, 0, 0)})

	// AC: a family application with a 22-year-old child fails the eligibility check.
	bad := sa.Must(201, "POST", "/api/v1/membership/applications", map[string]any{"typeId": famRes, "customerId": parent,
		"members": []map[string]any{{"customerId": spouse, "role": "spouse"}, {"customerId": child22, "role": "child"}}}).JSON()
	el := bad["eligibility"].(map[string]any)
	if el["eligible"] != false {
		t.Fatalf("22-year-old child must fail: %v", el)
	}
	if r := sa.Do("POST", "/api/v1/membership/applications/"+str(bad["id"])+":submit", nil); r.Status != 422 {
		t.Fatalf("ineligible submit: %s", r)
	}
	chk := sa.Must(200, "POST", "/api/v1/membership/eligibility:check", map[string]any{"typeId": famRes, "customerId": parent,
		"members": []map[string]any{{"customerId": spouse, "role": "spouse"}, {"customerId": child10, "role": "child"}}}).JSON()
	if chk["eligible"] != true {
		t.Fatalf("eligible family: %v", chk)
	}
	// Application → approval (no workflow: approved) → fee folio → payment → activation.
	app := sa.Must(201, "POST", "/api/v1/membership/applications", map[string]any{"typeId": famRes, "customerId": parent, "submit": true,
		"members": []map[string]any{{"customerId": spouse, "role": "spouse"}, {"customerId": child10, "role": "child"}}}).JSON()
	app = sa.Must(200, "GET", "/api/v1/membership/applications/"+str(app["id"]), nil).JSON()
	if app["status"] != "approved" || app["folioId"] == nil {
		t.Fatalf("approved with fee folio: %v", app)
	}
	if r := sa.Do("POST", "/api/v1/membership/applications/"+str(app["id"])+":activate", map[string]any{}); r.Status != 409 {
		t.Fatalf("activation before payment must fail: %s", r)
	}
	sa.Must(201, "POST", "/api/v1/billing/folios/"+str(app["folioId"])+"/payments", map[string]any{"methodType": "bank_transfer", "amount": "8000000", "reference": "TRF-MB1"})
	var msID string
	waitFor(t, 20*time.Second, "activation after payment", func() bool {
		a := sa.Must(200, "GET", "/api/v1/membership/applications/"+str(app["id"]), nil).JSON()
		msID = str(a["membershipId"])
		return a["status"] == "activated"
	})
	ms := sa.Must(200, "GET", "/api/v1/membership/memberships/"+msID, nil).JSON()
	if ms["status"] != "active" || len(ms["members"].([]any)) != 3 || len(ms["fees"].([]any)) != 2 {
		t.Fatalf("membership: %v", ms)
	}
	// Multi-program: the same member also holds Golf; one card covers both.
	gm := sa.Must(201, "POST", "/api/v1/membership/memberships", map[string]any{"typeId": golfInd, "customerId": parent, "startDate": dateAgo(0, 8, 0)}).JSON()
	ent := sa.Must(200, "GET", "/api/v1/membership/entitlements?customerId="+parent, nil).JSON()
	if ent["active"] != true || len(ent["memberships"].([]any)) != 2 {
		t.Fatalf("multi-program: %v", ent)
	}
	cards := sa.Must(200, "GET", "/api/v1/membership/cards?filter[customerId]="+parent, nil).Items()
	if len(cards) != 1 {
		t.Fatalf("one card for all programs: %v", cards)
	}
	look := sa.Must(200, "GET", "/api/v1/membership/cards:lookup?code="+str(cards[0]["qrToken"]), nil).JSON()
	if len(look["memberships"].([]any)) != 2 {
		t.Fatalf("card lookup: %v", look)
	}
	// AC: pause 3 months extends validity 3 months and disables Member Rate.
	gid := str(gm["id"])
	endBefore, _ := time.Parse("2006-01-02", str(gm["endDate"])[:10])
	from := time.Now().Format("2006-01-02")
	until := time.Now().AddDate(0, 3, 0).Format("2006-01-02")
	pr := sa.Must(202, "POST", "/api/v1/membership/memberships/"+gid+":pause", map[string]any{"from": from, "until": until, "reason": "overseas assignment"}).JSON()
	if pr["status"] != "applied" && pr["status"] != "approved" {
		t.Fatalf("pause request: %v", pr)
	}
	paused := sa.Must(200, "GET", "/api/v1/membership/memberships/"+gid, nil).JSON()
	endAfter, _ := time.Parse("2006-01-02", str(paused["endDate"])[:10])
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
	if old := sa.Must(200, "GET", "/api/v1/membership/cards?filter[customerId]="+parent+"&filter[status]=replaced", nil).Items(); len(old) != 1 {
		t.Fatalf("old card replaced: %v", old)
	}
	child2 := customer(t, sa, "MB-CHILD2", "Dio Santoso", map[string]any{"birthDate": dateAgo(15, 0, 0)})
	withChild := sa.Must(201, "POST", "/api/v1/membership/memberships/"+msID+"/members", map[string]any{"customerId": child2, "role": "child"}).JSON()
	var rowID string
	for _, m := range withChild["members"].([]any) {
		mm := m.(map[string]any)
		if mm["customerId"] == child2 {
			rowID = str(mm["id"])
		}
	}
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+msID+"/members/"+rowID+":remove", map[string]any{"reason": "moved abroad"})

	// AC: annual fee unpaid after the grace period → Suspended, member rate and member charge refused.
	late := customer(t, sa, "MB-LATE", "Late Payer", map[string]any{"email": "late@p2.test"})
	lm := sa.Must(201, "POST", "/api/v1/membership/memberships", map[string]any{"typeId": golfInd, "customerId": late,
		"startDate": dateAgo(1, 2, 0), "endDate": dateAgo(0, 0, 40)}).JSON()
	lid := str(lm["id"])
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
	sa.Must(201, "POST", "/api/v1/billing/folios/"+str(fees[0]["folioId"])+"/payments", map[string]any{"methodType": "cash", "amount": fees[0]["amount"]})
	waitFor(t, 20*time.Second, "lift after payment", func() bool {
		return sa.Must(200, "GET", "/api/v1/membership/memberships/"+lid, nil).JSON()["status"] == "active"
	})
	renewed := sa.Must(200, "GET", "/api/v1/membership/memberships/"+lid, nil).JSON()
	if end, _ := time.Parse("2006-01-02", str(renewed["endDate"])[:10]); !end.After(time.Now()) {
		t.Fatalf("paid annual fee must extend validity: %v", renewed["endDate"])
	}
	// Discipline suspension → lift; suspend → reactivation (approval, fee); postpone; renew; waive; cancel.
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+lid+":suspend", map[string]any{"reason": "conduct"})
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+lid+":lift-suspension", map[string]any{"reason": "appeal accepted"})
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+lid+":suspend", map[string]any{"reason": "conduct again"})
	sa.Must(202, "POST", "/api/v1/membership/memberships/"+lid+":reactivate", map[string]any{"reason": "board decision"})
	if st := sa.Must(200, "GET", "/api/v1/membership/memberships/"+lid, nil).JSON()["status"]; st != "active" {
		t.Fatalf("reactivated: %v", st)
	}
	nd := time.Now().AddDate(1, 2, 0).Format("2006-01-02")
	sa.Must(202, "POST", "/api/v1/membership/memberships/"+lid+":postpone", map[string]any{"dueDate": nd, "reason": "hardship"})
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+lid+":renew", nil)
	due := sa.Must(200, "GET", "/api/v1/membership/annual-fees?filter[membershipId]="+lid+"&filter[status]=due,postponed", nil).Items()
	if len(due) == 0 {
		t.Fatal("renewal fee")
	}
	sa.Must(200, "POST", "/api/v1/membership/annual-fees/"+str(due[0]["id"])+":waive", map[string]any{"reason": "goodwill"})
	sa.Must(200, "POST", "/api/v1/membership/memberships/"+lid+":cancel", map[string]any{"reason": "member resigned"})
	if n := len(sa.Must(200, "GET", "/api/v1/membership/requests", nil).Items()); n < 4 {
		t.Fatalf("requests: %d", n)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/membership/memberships?filter[programKind]=golf", nil).Items()); n < 2 {
		t.Fatalf("list memberships %d", n)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/membership/applications?filter[status]=activated", nil).Items()); n != 1 {
		t.Fatalf("applications %d", n)
	}
}
