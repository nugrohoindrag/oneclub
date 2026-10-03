package e2e

// P1 back office: course structure (EP-02), golf resources (EP-09/10/11),
// pricing configuration (EP-08), billing & payment (EP-12), CRM (EP-01).

import (
	"testing"
	"time"
)

// FR-CRS-01..06: a course is structured in sections, holes, tee sets and
// playing routes (holes derived from the sections).
func TestP1CourseStructure(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	var venue string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.venues WHERE property_id = $1 AND code = 'GOLF'`, []any{inst.Main}, &venue)
	c := gm.Must(201, "POST", "/api/v1/golf/courses", map[string]any{"venueId": venue, "code": "ACADEMY9", "name": "Par-3 Academy Course", "holes": 9}).JSON()
	cid := str(c["id"])
	sec := gm.Must(201, "POST", "/api/v1/golf/course-sections", map[string]any{"courseId": cid, "code": "ALL", "name": "All Nine", "sequence": 1}).JSON()
	gm.Must(200, "PATCH", "/api/v1/golf/course-sections/"+str(sec["id"]), map[string]any{"name": "Academy Nine"})
	ts := gm.Must(201, "POST", "/api/v1/golf/tee-sets", map[string]any{"courseId": cid, "code": "GOLD", "name": "Gold", "color": "gold", "courseRating": "54.0", "slope": 90, "gender": "any"}).JSON()
	gm.Must(200, "PATCH", "/api/v1/golf/tee-sets/"+str(ts["id"]), map[string]any{"slope": 92})
	var holes []string
	for n := 1; n <= 9; n++ {
		h := gm.Must(201, "POST", "/api/v1/golf/holes", map[string]any{"courseId": cid, "sectionId": sec["id"], "code": "A0" + string(rune('0'+n)), "number": n, "par": 3,
			"strokeIndex": n, "distances": map[string]any{"GOLD": 90 + n*5}}).JSON()
		holes = append(holes, str(h["id"]))
	}
	gm.Must(200, "PATCH", "/api/v1/golf/holes/"+holes[0], map[string]any{"description": "Island green"})
	rt := gm.Must(201, "POST", "/api/v1/golf/playing-routes", map[string]any{"courseId": cid, "code": "ACAD9", "name": "Academy Nine", "sectionCodes": "ALL", "isDefault": true}).JSON()
	rh := gm.Must(200, "GET", "/api/v1/golf/playing-routes/"+str(rt["id"])+"/holes", nil).JSON()
	if rh["holeCount"].(float64) != 9 || rh["par"].(float64) != 27 {
		t.Fatalf("route holes: %v", rh)
	}
	gm.Must(200, "PATCH", "/api/v1/golf/playing-routes/"+str(rt["id"]), map[string]any{"name": "Academy 9"})
	as := gm.Must(201, "POST", "/api/v1/golf/course-assets", map[string]any{"courseId": cid, "code": "A01-GC", "assetType": "green_center",
		"name": "Green centre A01", "geometry": map[string]any{"type": "Point", "coordinates": []float64{106.6527, -6.2001}}}).JSON()
	gm.Must(200, "PATCH", "/api/v1/golf/course-assets/"+str(as["id"]), map[string]any{"name": "Green centre hole 1"})

	// remove the temporary structure (children first)
	gm.Must(204, "DELETE", "/api/v1/golf/course-assets/"+str(as["id"]), nil)
	gm.Must(204, "DELETE", "/api/v1/golf/playing-routes/"+str(rt["id"]), nil)
	for _, h := range holes {
		gm.Must(204, "DELETE", "/api/v1/golf/holes/"+h, nil)
	}
	gm.Must(204, "DELETE", "/api/v1/golf/tee-sets/"+str(ts["id"]), nil)
	// the section is still referenced by the archived holes: set it inactive
	gm.Must(409, "DELETE", "/api/v1/golf/course-sections/"+str(sec["id"]), nil)
	gm.Must(200, "PATCH", "/api/v1/golf/course-sections/"+str(sec["id"]), map[string]any{"status": "inactive"})
	sec2 := gm.Must(201, "POST", "/api/v1/golf/course-sections", map[string]any{"courseId": cid, "code": "SPARE", "name": "Spare"}).JSON()
	gm.Must(204, "DELETE", "/api/v1/golf/course-sections/"+str(sec2["id"]), nil)

	// golf resources
	cd := gm.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C900", "name": "Zahra Trainee", "gender": "female", "partnershipStatus": "trainee"}).JSON()
	gm.Must(200, "PATCH", "/api/v1/golf/caddies/"+str(cd["id"]), map[string]any{"partnershipStatus": "partner"})
	gm.Must(204, "DELETE", "/api/v1/golf/caddies/"+str(cd["id"]), nil)
	gc := gm.Must(201, "POST", "/api/v1/golf/golf-carts", map[string]any{"code": "B90", "name": "Buggy 90", "cartType": "electric", "capacity": 2}).JSON()
	gm.Must(200, "PATCH", "/api/v1/golf/golf-carts/"+str(gc["id"]), map[string]any{"name": "Buggy 90 (new)"})
	gm.Must(204, "DELETE", "/api/v1/golf/golf-carts/"+str(gc["id"]), nil)
	lk := gm.Must(201, "POST", "/api/v1/golf/lockers", map[string]any{"code": "V001", "name": "VIP Locker 1", "area": "male", "zone": "VIP"}).JSON()
	gm.Must(200, "PATCH", "/api/v1/golf/lockers/"+str(lk["id"]), map[string]any{"zone": "VIP Lounge"})
	gm.Must(204, "DELETE", "/api/v1/golf/lockers/"+str(lk["id"]), nil)
}

// FR-PRC-01..09: pricing dimensions and rules are configurable and
// effective-dated.
func TestP1PricingConfiguration(t *testing.T) {
	fin := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	dt := fin.Must(201, "POST", "/api/v1/commercial/day-types", map[string]any{"code": "TOURNEY", "name": "Tournament Day", "weekdays": "", "priority": 10}).JSON()
	fin.Must(200, "PATCH", "/api/v1/commercial/day-types/"+str(dt["id"]), map[string]any{"name": "Tournament"})
	tb := fin.Must(201, "POST", "/api/v1/commercial/time-bands", map[string]any{"code": "TWI", "name": "Twilight", "session": "other", "startTime": "15:00", "endTime": "16:00"}).JSON()
	fin.Must(200, "PATCH", "/api/v1/commercial/time-bands/"+str(tb["id"]), map[string]any{"name": "Twilight (late)"})
	future := time.Now().AddDate(1, 0, 0).Format("2006-01-02")
	rp := fin.Must(201, "POST", "/api/v1/commercial/rate-plans", map[string]any{"code": "RC2027", "name": "Rate Card 2027", "pricingMode": "nett", "effectiveFrom": future}).JSON()
	fin.Must(200, "PATCH", "/api/v1/commercial/rate-plans/"+str(rp["id"]), map[string]any{"description": "Draft for next year"})
	rule := fin.Must(201, "POST", "/api/v1/commercial/pricing-rules", map[string]any{"ratePlanId": rp["id"], "code": "MEMBER-2027", "name": "Member 2027",
		"chargeType": "golf_round", "segment": "member", "price": "690000", "effectiveFrom": future, "priority": 50,
		"components": []map[string]any{{"code": "green_fee", "name": "Green Fee", "type": "remainder"}, {"code": "caddy_fee", "name": "Caddy Fee", "type": "amount", "value": "160000", "liability": true}}}).JSON()
	fin.Must(422, "POST", "/api/v1/commercial/pricing-rules", map[string]any{"ratePlanId": rp["id"], "code": "BAD", "name": "Bad", "chargeType": "golf_round",
		"price": "100", "effectiveFrom": future, "components": []map[string]any{{"code": "x", "name": "X", "type": "percent", "value": "150"}}})
	fin.Must(200, "PATCH", "/api/v1/commercial/pricing-rules/"+str(rule["id"]), map[string]any{"price": "695000"})
	// the current rate card is untouched by the future plan
	r := fin.Must(200, "POST", "/api/v1/commercial/pricing:resolve", map[string]any{"date": clubDay(inst, 2, isWeekday), "time": "06:00", "segments": []string{"member"}}).JSON()
	eqAmount(t, "current member rate", r["total"], 640000)
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	pa.Must(409, "DELETE", "/api/v1/commercial/rate-plans/"+str(rp["id"]), nil) // has rules
	rp2 := fin.Must(201, "POST", "/api/v1/commercial/rate-plans", map[string]any{"code": "TMP", "name": "Temporary", "pricingMode": "plus_plus", "effectiveFrom": future}).JSON()
	pa.Must(204, "DELETE", "/api/v1/commercial/rate-plans/"+str(rp2["id"]), nil)
	pa.Must(204, "DELETE", "/api/v1/commercial/time-bands/"+str(tb["id"]), nil)
	pa.Must(204, "DELETE", "/api/v1/commercial/day-types/"+str(dt["id"]), nil)
}

// EP-12: folio, charges, void, payments (cash, online cancel), deposit,
// refund with approval, receipt, reconciliation and accounting export.
func TestP1BillingAndPayment(t *testing.T) {
	cashier := login(t, inst, "cashier@demo.oneclub.id", demoPassword)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	cust := demoMemberCustomer(t, inst)

	f := cashier.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "Hendra Member", "customerId": cust, "sourceRef": "Pro shop"}).JSON()
	fid := str(f["id"])
	cashier.Must(201, "POST", "/api/v1/billing/folios/"+fid+"/lines", map[string]any{"chargeType": "other", "description": "Golf balls", "unitPrice": "150000", "quantity": "2"})
	fd := cashier.Must(201, "POST", "/api/v1/billing/folios/"+fid+"/lines", map[string]any{"chargeType": "locker", "description": "Locker (day)", "unitPrice": "50000"}).JSON()
	var l2 map[string]any
	for _, l := range fd["lines"].([]any) {
		if l.(map[string]any)["chargeType"] == "locker" {
			l2 = l.(map[string]any)
		}
	}
	cashier.Must(403, "POST", "/api/v1/billing/folios/"+fid+"/lines/"+str(l2["id"])+":void", map[string]any{"reason": "x"})
	fin.Must(200, "POST", "/api/v1/billing/folios/"+fid+"/lines/"+str(l2["id"])+":void", map[string]any{"reason": "Locker not used"})
	// a pending online payment can be cancelled
	op := cashier.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": fid, "amount": "100000", "methodType": "qris", "channel": "online"}).JSON()
	cashier.Must(200, "POST", "/api/v1/billing/payments/"+str(op["id"])+":cancel", map[string]any{"reason": "Customer pays cash"})
	// deposit then cash for the rest
	dp := cashier.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": fid, "amount": "100000", "methodType": "cash", "channel": "venue", "purpose": "deposit"}).JSON()
	_ = dp
	detail := cashier.Must(200, "GET", "/api/v1/billing/folios/"+fid, nil).JSON()
	deps := detail["deposits"].([]any)
	if len(deps) != 1 {
		t.Fatalf("deposits: %v", detail["deposits"])
	}
	cashier.Must(200, "POST", "/api/v1/billing/deposits/"+str(deps[0].(map[string]any)["id"])+":apply", nil)
	pay := cashier.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": fid, "amount": "200000", "methodType": "cash", "channel": "venue"}).JSON()
	cashier.OK("POST", "/api/v1/billing/payments/"+str(pay["id"])+":send-receipt", map[string]any{"email": "member@demo.oneclub.id"})
	detail = cashier.Must(200, "GET", "/api/v1/billing/folios/"+fid, nil).JSON()
	eqAmount(t, "folio balance", detail["summary"].(map[string]any)["balance"], 0)
	cashier.Must(200, "POST", "/api/v1/billing/folios/"+fid+":close", nil)
	fin.Must(422, "POST", "/api/v1/billing/folios/"+fid+":reopen", map[string]any{})
	fin.Must(200, "POST", "/api/v1/billing/folios/"+fid+":reopen", map[string]any{"reason": "Refund one box of balls"})

	// refund needs approval by the Finance Manager (FR-PAY-05)
	cashier.Must(403, "POST", "/api/v1/billing/refunds", map[string]any{"paymentId": pay["id"], "amount": "1", "reason": "x"})
	rf := login(t, inst, "property.admin@demo.oneclub.id", demoPassword).Must(201, "POST", "/api/v1/billing/refunds", map[string]any{"paymentId": pay["id"], "amount": "150000", "reason": "Returned golf balls",
		"destination": "original_method"}).JSON()
	if rf["status"] != "pending" || rf["approvalRequestId"] == nil {
		t.Fatalf("refund waits for approval: %v", rf)
	}
	fin.Must(200, "POST", "/api/v1/platform/approvals/"+str(rf["approvalRequestId"])+":approve", map[string]any{})
	waitFor(t, 10*time.Second, "refund processed", func() bool {
		for _, r := range fin.Must(200, "GET", "/api/v1/billing/refunds", nil).Items() {
			if r["id"] == rf["id"] && r["status"] == "processed" {
				return true
			}
		}
		return false
	})

	// customer account (non-member) with opening balance
	other := newCustomer(t, login(t, inst, "membership.admin@demo.oneclub.id", demoPassword), "CUS-ACC-0001", "Wira Corporate", "", "1985-02-02", "male")
	acc := fin.Must(201, "POST", "/api/v1/billing/customer-accounts", map[string]any{"customerId": other, "accountType": "customer", "creditLimit": "5000000"}).JSON()
	fin.Must(201, "POST", "/api/v1/billing/customer-accounts/"+str(acc["id"])+"/entries", map[string]any{"entryType": "opening_balance", "amount": "750000",
		"description": "Opening balance from Rhapsody"})

	// reconciliation against a settlement report (FR-PAY-08)
	today := time.Now().In(clubLoc(inst)).Format("2006-01-02")
	rec := fin.Must(201, "POST", "/api/v1/billing/reconciliations", map[string]any{"date": today, "integrationCode": "mock-payment",
		"settlement": []map[string]any{{"externalId": "unknown-txn-1", "amount": "123000", "status": "settled"}}}).JSON()
	var item string
	for _, it := range rec["items"].([]any) {
		im := it.(map[string]any)
		if im["result"] == "missing_in_oneclub" {
			item = str(im["id"])
		}
	}
	if item == "" {
		t.Fatalf("reconciliation should flag the unknown settlement row: %v", rec["items"])
	}
	fin.Must(200, "POST", "/api/v1/billing/reconciliation-items/"+item+":resolve", map[string]any{"note": "Belongs to another merchant account"})
	fin.OK("POST", "/api/v1/billing/accounting-exports", map[string]any{"date": today})
	fin.Must(200, "GET", "/api/v1/reporting/reports/billing.refunds", nil)
	fin.Must(200, "GET", "/api/v1/reporting/reports/billing.outstanding_member_charges", nil)
}

// EP-01: corporate accounts, nominees, family, preferences, duplicates,
// merge, guest upgrade, Customer 360, history, personal data export/erase.
func TestP1CustomerProfiles(t *testing.T) {
	ma := login(t, inst, "membership.admin@demo.oneclub.id", demoPassword)
	mm := login(t, inst, "membership@demo.oneclub.id", demoPassword)
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	a := newCustomer(t, ma, "CUS-CRM-0001", "Agung Profil", "agung@example.test", "1979-11-11", "male")
	b := newCustomer(t, ma, "CUS-CRM-0002", "Bunga Profil", "", "1983-06-06", "female")
	dupe := newCustomer(t, ma, "CUS-CRM-0003", "Agung P.", "agung@example.test", "1979-11-11", "male")

	corp := ma.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "PT-MAJU", "name": "PT Maju Bersama", "npwp": "01.234.567.8-901.000",
		"contactName": "Agung Profil", "email": "hr@maju.example.test"}).JSON()
	ma.Must(200, "PATCH", "/api/v1/crm/corporate-accounts/"+str(corp["id"]), map[string]any{"address": "Jl. Sudirman 1, Jakarta"})
	nom := ma.Must(201, "POST", "/api/v1/crm/corporate-nominees", map[string]any{"corporateAccountId": corp["id"], "customerId": a, "title": "Director"}).JSON()
	ma.Must(200, "PATCH", "/api/v1/crm/corporate-nominees/"+str(nom["id"]), map[string]any{"title": "President Director"})
	rel := ma.Must(201, "POST", "/api/v1/crm/customer-relationships", map[string]any{"customerId": a, "relatedCustomerId": b, "relationship": "spouse"}).JSON()
	ma.Must(200, "PATCH", "/api/v1/crm/customer-relationships/"+str(rel["id"]), map[string]any{"notes": "Married 2008"})
	pref := ma.Must(201, "POST", "/api/v1/crm/customer-preferences", map[string]any{"customerId": a, "category": "caddy", "key": "preferred_caddy", "value": "C003"}).JSON()
	ma.Must(200, "PATCH", "/api/v1/crm/customer-preferences/"+str(pref["id"]), map[string]any{"value": "C004"})

	// duplicates and merge (FR-CUS-05/06)
	d := ma.Must(200, "GET", "/api/v1/crm/customers:duplicates?email=agung@example.test&excludeId="+a, nil).Items()
	if len(d) != 1 || str(d[0]["id"]) != dupe {
		t.Fatalf("duplicates: %v", d)
	}
	mm.Must(200, "POST", "/api/v1/crm/customers:merge", map[string]any{"sourceId": dupe, "targetId": a, "reason": "Same person (e-mail and birth date)"})

	// Customer 360 and history (FR-CUS-03/04)
	ov := ma.Must(200, "GET", "/api/v1/crm/customers/"+a+"/overview", nil).JSON()
	if len(ov["relationships"].([]any)) != 1 || len(ov["preferences"].([]any)) != 1 {
		t.Fatalf("customer 360: %v", ov)
	}
	member := ma.Must(200, "GET", "/api/v1/crm/customers/"+demoMemberCustomer(t, inst)+"/overview", nil).JSON()
	if len(member["memberships"].([]any)) == 0 || member["handicapIndex"] == nil {
		t.Fatalf("member 360 should show membership and handicap: %v", member)
	}
	ma.Must(200, "GET", "/api/v1/crm/customers/"+demoMemberCustomer(t, inst)+"/history?kind=round", nil)
	ma.Must(200, "GET", "/api/v1/crm/customers/"+dupe+"/overview", nil) // a merged profile resolves to its target

	// guest upgrade keeps history (FR-CUS-02)
	g := ma.Must(201, "POST", "/api/v1/crm/guests", map[string]any{"code": "G-CRM-0001", "name": "Candra Tamu", "phone": "+628129990060"}).JSON()
	up := ma.Must(201, "POST", "/api/v1/crm/guests/"+str(g["id"])+":upgrade", map[string]any{"code": "CUS-CRM-0004", "email": "candra@example.test", "consent": true}).JSON()

	// personal data export & erase (FR-CUS-09)
	ex := pa.Must(200, "POST", "/api/v1/crm/customers/"+str(up["id"])+":export-personal-data", nil).JSON()
	if ex["file"].(map[string]any)["url"] == "" {
		t.Fatalf("export: %v", ex)
	}
	pa.Must(200, "GET", str(ex["file"].(map[string]any)["url"]), nil)
	pa.Must(200, "POST", "/api/v1/crm/customers/"+str(up["id"])+":erase", map[string]any{"reason": "Data subject request #12"})
	pa.Must(200, "GET", "/api/v1/crm/data-requests", nil)

	// clean-up of the relationship records
	pa.Must(204, "DELETE", "/api/v1/crm/customer-preferences/"+str(pref["id"]), nil)
	pa.Must(204, "DELETE", "/api/v1/crm/customer-relationships/"+str(rel["id"]), nil)
	pa.Must(204, "DELETE", "/api/v1/crm/corporate-nominees/"+str(nom["id"]), nil)
	tmp := ma.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "PT-TMP", "name": "PT Sementara"}).JSON()
	pa.Must(204, "DELETE", "/api/v1/crm/corporate-accounts/"+str(tmp["id"]), nil)
}
