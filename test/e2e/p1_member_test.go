package e2e

// P1 membership lifecycle (EP-06/07) and the Member Portal (EP-13): OTP
// login, Book Golf, my bookings, profile and membership application.

import (
	"regexp"
	"testing"
	"time"
)

func typeIDs(t *testing.T, c *Client) (types, packages map[string]string) {
	types, packages = map[string]string{}, map[string]string{}
	for _, x := range c.Must(200, "GET", "/api/v1/membership/types?limit=100", nil).Items() {
		types[str(x["code"])] = str(x["id"])
	}
	for _, x := range c.Must(200, "GET", "/api/v1/membership/packages?limit=100", nil).Items() {
		packages[str(x["code"])] = str(x["id"])
	}
	return
}

func newCustomer(t *testing.T, c *Client, code, name, email, birth, gender string) string {
	t.Helper()
	body := map[string]any{"code": code, "name": name, "phone": "+62812" + code[len(code)-4:] + "0000", "birthDate": birth, "gender": gender,
		"duplicateAcknowledged": true}
	if email != "" {
		body["email"] = email
	}
	return str(c.Must(201, "POST", "/api/v1/crm/customers", body).JSON()["id"])
}

// FR-MEM-01..09 AC: application → eligibility (a 22-year-old child fails
// the Family type) → approval → fee payment → activation → card, portal
// invite and renewal.
func TestP1MembershipLifecycle(t *testing.T) {
	admin := login(t, inst, "membership.admin@demo.oneclub.id", demoPassword)
	mgr := login(t, inst, "membership@demo.oneclub.id", demoPassword)
	cashier := login(t, inst, "cashier@demo.oneclub.id", demoPassword)
	types, packages := typeIDs(t, admin)
	now := time.Now()
	principal := newCustomer(t, admin, "CUS-FAM-0001", "Rudi Family", "rudi.family@example.test", "1980-03-04", "male")
	spouse := newCustomer(t, admin, "CUS-FAM-0002", "Sinta Family", "", "1982-07-09", "female")
	child22 := newCustomer(t, admin, "CUS-FAM-0003", "Tara Family", "", now.AddDate(-22, -1, 0).Format("2006-01-02"), "female")
	child10 := newCustomer(t, admin, "CUS-FAM-0004", "Umar Family", "", now.AddDate(-10, 0, 0).Format("2006-01-02"), "male")

	app := admin.Must(201, "POST", "/api/v1/membership/applications", map[string]any{"customerId": principal, "typeId": types["FAM"], "packageId": packages["FAM-1Y"],
		"dependents": []map[string]any{{"customerId": spouse, "relationship": "spouse"}, {"customerId": child22, "relationship": "child"}},
		"documents":  []map[string]any{{"kind": "family_card", "verified": true}}}).JSON()
	aid := str(app["id"])
	el := admin.Must(200, "POST", "/api/v1/membership/applications/"+aid+":check-eligibility", nil).JSON()["eligibility"].(map[string]any)
	if el["eligible"] != false {
		t.Fatalf("a 22-year-old child must fail the Family eligibility: %v", el)
	}
	admin.Must(422, "POST", "/api/v1/membership/applications/"+aid+":submit", nil)
	admin.Must(200, "PATCH", "/api/v1/membership/applications/"+aid, map[string]any{"dependents": []map[string]any{
		{"customerId": spouse, "relationship": "spouse"}, {"customerId": child10, "relationship": "child"}}, "notes": "Child 22 removed"})
	el = admin.Must(200, "POST", "/api/v1/membership/applications/"+aid+":check-eligibility", nil).JSON()["eligibility"].(map[string]any)
	if el["eligible"] != true {
		t.Fatalf("eligibility: %v", el)
	}
	app = admin.Must(200, "POST", "/api/v1/membership/applications/"+aid+":submit", nil).JSON()
	if app["status"] != "pending" || app["approvalRequestId"] == nil {
		t.Fatalf("submit: %v", app)
	}
	mgr.Must(200, "POST", "/api/v1/platform/approvals/"+str(app["approvalRequestId"])+":approve", map[string]any{})
	app = admin.Must(200, "GET", "/api/v1/membership/applications/"+aid, nil).JSON()
	if app["status"] != "approved" || app["feeFolioId"] == nil {
		t.Fatalf("approved application should have a fee folio: %v", app)
	}
	mgr.Must(409, "POST", "/api/v1/membership/applications/"+aid+":activate", map[string]any{})
	folio := cashier.Must(200, "GET", "/api/v1/billing/folios/"+str(app["feeFolioId"]), nil).JSON()
	eqAmount(t, "joining + annual fee", folio["summary"].(map[string]any)["balance"], 75000000+18000000)
	cashier.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": app["feeFolioId"], "amount": "93000000", "methodType": "bank_transfer",
		"channel": "venue", "reference": "TRF-FAM-0001"})
	app = mgr.Must(200, "POST", "/api/v1/membership/applications/"+aid+":activate", map[string]any{}).JSON()
	if app["status"] != "completed" || app["membershipId"] == nil {
		t.Fatalf("activate: %v", app)
	}
	ms := mgr.Must(200, "GET", "/api/v1/membership/memberships?limit=200", nil).Items()
	var principalMembership, memberID string
	family := 0
	for _, m := range ms {
		if str(m["id"]) == str(app["membershipId"]) {
			principalMembership, memberID = str(m["id"]), str(m["memberId"])
		}
		if m["principalId"] != nil && str(m["principalId"]) == str(app["membershipId"]) {
			family++
		}
	}
	if principalMembership == "" || family != 2 {
		t.Fatalf("principal + 2 family memberships expected (family %d): %v", family, ms)
	}

	// Card, portal invite, self-activation and renewal.
	card := mgr.Must(201, "POST", "/api/v1/membership/cards", map[string]any{"memberId": memberID, "cardType": "physical", "legacyNumber": "RH-778899"}).JSON()
	mgr.Must(200, "POST", "/api/v1/membership/cards/"+str(card["id"])+":deactivate", map[string]any{"reason": "Lost card"})
	mgr.OK("POST", "/api/v1/membership/members/"+memberID+":invite", nil)
	anon(t, inst).OK("POST", "/api/v1/public/membership/activate", map[string]any{"memberNo": app["memberNo"], "email": "rudi.family@example.test", "birthDate": "1980-03-04"})
	ren := mgr.Must(201, "POST", "/api/v1/membership/memberships/"+principalMembership+":renew", map[string]any{"packageId": packages["FAM-1Y"]}).JSON()
	if ren["status"] != "pending" || ren["folioId"] == nil {
		t.Fatalf("renewal waits for payment: %v", ren)
	}

	// A draft application can be cancelled.
	other := newCustomer(t, admin, "CUS-IND-0005", "Vina Single", "vina@example.test", "1990-01-01", "female")
	d := admin.Must(201, "POST", "/api/v1/membership/applications", map[string]any{"customerId": other, "typeId": types["IND"], "packageId": packages["IND-1Y"]}).JSON()
	admin.Must(200, "POST", "/api/v1/membership/applications/"+str(d["id"])+":cancel", map[string]any{"reason": "Applicant withdrew"})

	// Reports (FR-RPT-06).
	for _, code := range []string{"membership.active_members", "membership.new_members", "membership.expiring"} {
		mgr.Must(200, "GET", "/api/v1/reporting/reports/"+code, nil)
	}
}

// FR-MEM-10: programs, types and packages are configurable.
func TestP1MembershipConfiguration(t *testing.T) {
	mgr := login(t, inst, "membership@demo.oneclub.id", demoPassword)
	p := mgr.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "TESTPRG", "name": "Test Program", "programKind": "sport_club"}).JSON()
	mgr.Must(200, "PATCH", "/api/v1/membership/programs/"+str(p["id"]), map[string]any{"description": "Not operational in P1"})
	ty := mgr.Must(201, "POST", "/api/v1/membership/types", map[string]any{"programId": p["id"], "code": "TESTTYPE", "name": "Test Type", "category": "individual",
		"eligibility": map[string]any{"minAge": 21, "maxAge": 35}}).JSON()
	mgr.Must(422, "PATCH", "/api/v1/membership/types/"+str(ty["id"]), map[string]any{"eligibility": map[string]any{"minAge": 40, "maxAge": 30}})
	mgr.Must(200, "PATCH", "/api/v1/membership/types/"+str(ty["id"]), map[string]any{"maxGuests": 2})
	pk := mgr.Must(201, "POST", "/api/v1/membership/packages", map[string]any{"typeId": ty["id"], "code": "TEST-6M", "name": "Six months", "periodUnit": "month",
		"periodCount": 6, "joiningFee": "0", "periodFee": "3000000"}).JSON()
	mgr.Must(200, "PATCH", "/api/v1/membership/packages/"+str(pk["id"]), map[string]any{"periodFee": "3500000"})
	mgr.Must(204, "DELETE", "/api/v1/membership/packages/"+str(pk["id"]), nil)
	// configuration in use is set Inactive instead of deleted
	mgr.Must(409, "DELETE", "/api/v1/membership/types/"+str(ty["id"]), nil)
	mgr.Must(200, "PATCH", "/api/v1/membership/types/"+str(ty["id"]), map[string]any{"status": "inactive"})
	ty2 := mgr.Must(201, "POST", "/api/v1/membership/types", map[string]any{"programId": p["id"], "code": "TESTTYPE2", "name": "Unused Type", "category": "individual"}).JSON()
	mgr.Must(204, "DELETE", "/api/v1/membership/types/"+str(ty2["id"]), nil)
	p2 := mgr.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "TESTPRG2", "name": "Unused Program", "programKind": "residence"}).JSON()
	mgr.Must(204, "DELETE", "/api/v1/membership/programs/"+str(p2["id"]), nil)
}

// FR-APP-01..06: OTP login, Book Golf with a guest (member charge),
// reschedule and cancel from the Member Portal, profile and application.
func TestP1MemberPortal(t *testing.T) {
	m := anon(t, inst)
	m.Must(202, "POST", "/api/v1/auth/otp/request", map[string]any{"email": "member@demo.oneclub.id", "channel": "email"})
	var body string
	waitFor(t, 10*time.Second, "login code", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.notification_deliveries d JOIN platform.users u ON u.id = d.user_id
			WHERE u.email = 'member@demo.oneclub.id' AND d.event_code = 'auth.login_code' AND d.channel = 'email'`, nil, &n)
		if n == 0 {
			return false
		}
		sysQueryRow(t, inst, `SELECT d.body FROM platform.notification_deliveries d JOIN platform.users u ON u.id = d.user_id
			WHERE u.email = 'member@demo.oneclub.id' AND d.event_code = 'auth.login_code' AND d.channel = 'email' ORDER BY d.created_at DESC LIMIT 1`, nil, &body)
		return true
	})
	code := regexp.MustCompile(`\b(\d{6})\b`).FindStringSubmatch(body)
	if code == nil {
		t.Fatalf("no code in %q", body)
	}
	m.Must(422, "POST", "/api/v1/auth/otp/verify", map[string]any{"email": "member@demo.oneclub.id", "code": "000000"})
	m.Must(200, "POST", "/api/v1/auth/otp/verify", map[string]any{"email": "member@demo.oneclub.id", "code": code[1]})

	course := demoCourse(t, inst)
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	day := clubDay(inst, 8, isWeekday)
	am := slotsOf(teeTimes(t, gm, course, day), "morning", 1)
	m.Must(200, "GET", "/api/v1/member/golf/courses", nil)
	avail := m.Must(200, "GET", "/api/v1/member/golf/availability?date="+day+"&courseId="+course, nil).Items()
	if len(avail) == 0 {
		t.Fatal("member availability empty")
	}
	h := m.Must(201, "POST", "/api/v1/member/golf/holds", map[string]any{"teeTimeId": am[6]["id"], "players": 2}).JSON()
	m.OK("DELETE", "/api/v1/member/golf/holds/"+str(h["id"]), nil)
	h = m.Must(201, "POST", "/api/v1/member/golf/holds", map[string]any{"teeTimeId": am[6]["id"], "players": 2}).JSON()
	b := m.Must(201, "POST", "/api/v1/member/golf/bookings", map[string]any{"bookingType": "member", "holdId": h["id"], "teeTimeId": am[6]["id"],
		"players": []map[string]any{{"playerType": "member", "memberNo": "D0001"},
			{"playerType": "guest_of_member", "name": "Wawan Tamu", "phone": "+628129990040", "hostIndex": 0}}}).JSON()
	if b["status"] != "confirmed" || b["paymentMode"] != "member_charge" {
		t.Fatalf("member booking should be confirmed on member charge: %v %v", b["status"], b["paymentMode"])
	}
	m.Must(200, "GET", "/api/v1/member/bookings", nil)
	m.Must(200, "POST", "/api/v1/member/bookings/"+str(b["id"])+":reschedule", map[string]any{"teeTimeId": am[8]["id"], "reason": "Earlier meeting"})
	m.Must(200, "POST", "/api/v1/member/bookings/"+str(b["id"])+":cancel", map[string]any{"reason": "Out of town"})
	m.Must(200, "PATCH", "/api/v1/member/profile", map[string]any{"phone": "+628111000009", "marketingOptIn": true})

	// Membership application from the portal (FR-APP-06).
	types, packages := typeIDs(t, login(t, inst, "membership@demo.oneclub.id", demoPassword))
	app := m.Must(201, "POST", "/api/v1/member/membership-applications", map[string]any{"customerId": demoMemberCustomer(t, inst), "typeId": types["FAM"],
		"packageId": packages["FAM-1Y"]}).JSON()
	if app["channel"] != "member_portal" {
		t.Fatalf("portal application: %v", app)
	}

	// The member statement reflects the member charges (FR-PAY-06 AC).
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	period := time.Now().In(clubLoc(inst)).Format("2006-01")
	fin.OK("POST", "/api/v1/billing/member-statements:generate", map[string]any{"period": period})
}
