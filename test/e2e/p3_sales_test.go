package e2e

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/platform/integration"
)

func init() {
	// Team members reference users (no list endpoint for the generic test).
	resourceCRUDSkip["crm.sales_team_member"] = "covered by TestP3SalesLeads"
}

// slsUserID is the id of a user by e-mail.
func slsUserID(t *testing.T, email string) string {
	t.Helper()
	var uid string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = $1`, []any{email}, &uid)
	return uid
}

// slsDispatch runs the outbox until cond holds.
func slsDispatch(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitFor(t, 15*time.Second, what, func() bool {
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		return cond()
	})
}

// slsPipeline returns the id of a default pipeline and its stages by code.
func slsPipeline(t *testing.T, c *Client, code string) (string, map[string]string) {
	t.Helper()
	for _, p := range c.Must(200, "GET", "/api/v1/crm/pipelines?limit=100", nil).Items() {
		if p["code"] != code {
			continue
		}
		stages := map[string]string{}
		for _, s := range c.Must(200, "GET", "/api/v1/crm/pipeline-stages?filter[pipelineId]="+str(p["id"])+"&limit=100", nil).Items() {
			stages[str(s["code"])] = str(s["id"])
		}
		return str(p["id"]), stages
	}
	t.Fatalf("pipeline %s not seeded", code)
	return "", nil
}

// slsToken is the token of a quotation's public link.
func slsToken(t *testing.T, q map[string]any) string {
	t.Helper()
	link := str(q["publicLink"])
	if !strings.Contains(link, "/quotation/") {
		t.Fatalf("public link: %v", q["publicLink"])
	}
	return link[strings.LastIndex(link, "/")+1:]
}

// EP-01 (FR-LEAD-01..10) acceptance: manual leads with duplicate warning,
// round-robin assignment per line with the first-response SLA, activities
// and follow-ups, qualify / disqualify, conversion to customer, corporate
// account and opportunity, transfer between sales, and every automatic
// capture path — website inquiry and contact form, department mailbox,
// WhatsApp inbound webhook and the spreadsheet import (preview, commit,
// idempotent re-import).
func TestP3SalesLeads(t *testing.T) {
	sa := superAdmin(t, inst)
	sx := roleUser(t, inst, "sales_executive")
	bs := roleUser(t, inst, "banquet_sales")
	mk := roleUser(t, inst, "marketing_staff")
	sxID, bsID := slsUserID(t, "role.sales_executive@matrix.test"), slsUserID(t, "role.banquet_sales@matrix.test")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	loc := clubLoc(inst)

	sa.Must(200, "POST", "/api/v1/crm/pipelines:seed-defaults", nil)
	team := idOf(sa.Must(201, "POST", "/api/v1/crm/sales-teams", map[string]any{"code": "SLS" + sfx, "name": "Wedding Team " + sfx,
		"lines": []string{"wedding", "banquet"}}))
	sa.Must(201, "POST", "/api/v1/crm/sales-team-members", map[string]any{"teamId": team, "userId": sxID})
	sa.Must(201, "POST", "/api/v1/crm/sales-team-members", map[string]any{"teamId": team, "userId": bsID})
	extra := idOf(sa.Must(201, "POST", "/api/v1/crm/sales-team-members", map[string]any{"teamId": team,
		"userId": slsUserID(t, "role.crm_admin@matrix.test"), "status": "inactive"}))
	sa.Must(200, "PATCH", "/api/v1/crm/sales-team-members/"+extra, map[string]any{"lines": []string{"banquet"}})
	sa.Must(422, "PATCH", "/api/v1/crm/sales-team-members/"+extra, map[string]any{"lines": []string{"yacht"}})
	sa.Must(204, "DELETE", "/api/v1/crm/sales-team-members/"+extra, nil)
	if us := sx.Must(200, "GET", "/api/v1/crm/sales-users", nil).Items(); !containsID(us, sxID) || !containsID(us, bsID) {
		t.Fatalf("sales users: %v", us)
	}

	// Manual lead, auto-assigned round robin with the SLA due.
	phone := "+62812" + sfx
	a := mk.Must(201, "POST", "/api/v1/crm/leads", map[string]any{"name": "Andi " + sfx, "phone": phone, "email": "andi" + sfx + "@lead.test",
		"source": "instagram", "line": "wedding", "eventType": "wedding", "eventDate": time.Now().AddDate(0, 6, 0).Format("2006-01-02"), "pax": 300,
		"budget": "250000000", "companyName": "PT Andi Sejahtera " + sfx, "marketingConsent": true}).JSON()
	if a["status"] != "new" || a["firstResponseDueAt"] == nil || a["ownerUserId"] == nil || a["assignmentMethod"] != "round_robin" {
		t.Fatalf("new lead: %v", a)
	}
	due, _ := time.Parse(time.RFC3339, str(a["firstResponseDueAt"]))
	created, _ := time.Parse(time.RFC3339, str(a["createdAt"]))
	if !due.After(created) || due.Sub(created) > 14*time.Hour {
		t.Fatalf("SLA due %v for a lead created %v", due, created)
	}
	if h := created.In(loc).Hour(); h >= 8 && h < 19 && due.Sub(created) != time.Hour {
		t.Fatalf("within business hours the SLA is 1 hour: %v → %v", created, due)
	}
	// Duplicate warning until acknowledged.
	dup := mk.Do("POST", "/api/v1/crm/leads", map[string]any{"name": "Andi again", "phone": phone, "source": "walk_in", "line": "wedding"})
	if dup.Status != 409 || !strings.Contains(string(dup.Body), "duplicate_lead") {
		t.Fatalf("duplicate lead: %s", dup)
	}
	b := mk.Must(201, "POST", "/api/v1/crm/leads", map[string]any{"name": "Andi Sister " + sfx, "phone": phone, "source": "walk_in", "line": "wedding",
		"duplicateAcknowledged": true}).JSON()
	if str(b["ownerUserId"]) == str(a["ownerUserId"]) || !strings.Contains(sxID+bsID, str(b["ownerUserId"])) {
		t.Fatalf("round robin: %v then %v", a["ownerUserId"], b["ownerUserId"])
	}
	if d := mk.Must(200, "GET", "/api/v1/crm/leads/"+str(b["id"]), nil).JSON(); len(d["duplicates"].([]any)) == 0 {
		t.Fatalf("duplicates of the lead: %v", d["duplicates"])
	}
	// Assigning to another sales needs crm.lead.assign.
	sx.Must(403, "POST", "/api/v1/crm/leads", map[string]any{"name": "Not mine", "phone": "+62813" + sfx, "source": "phone", "ownerUserId": bsID})
	aid := str(a["id"])
	if str(a["ownerUserId"]) == sxID {
		sa.Must(200, "POST", "/api/v1/crm/leads/"+aid+":assign", map[string]any{"userId": bsID, "reason": "Balance the load"})
	}
	sa.Must(409, "POST", "/api/v1/crm/leads/"+aid+":assign", map[string]any{"userId": bsID})
	a = sa.Must(200, "POST", "/api/v1/crm/leads/"+aid+":assign", map[string]any{"userId": sxID, "reason": "VIP wedding"}).JSON()
	if str(a["ownerUserId"]) != sxID || len(a["assignments"].([]any)) < 2 {
		t.Fatalf("reassign: %v", a)
	}
	if items := sx.Must(200, "GET", "/api/v1/crm/leads?mine=true&filter[line]=wedding", nil).Items(); !containsID(items, aid) {
		t.Fatal("my leads miss the reassigned lead")
	}
	sx.Must(200, "PATCH", "/api/v1/crm/leads/"+aid, map[string]any{"notes": "Prefers garden venue", "pax": 350})

	// First response by a logged call; a follow-up completed, another cancelled.
	sx.Must(201, "POST", "/api/v1/crm/leads/"+aid+"/activities", map[string]any{"type": "call", "direction": "outbound", "subject": "Intro call"})
	if l := sx.Must(200, "GET", "/api/v1/crm/leads/"+aid, nil).JSON(); l["status"] != "contacted" || l["firstRespondedAt"] == nil {
		t.Fatalf("after the first call: %v", l)
	}
	fu := sx.Must(201, "POST", "/api/v1/crm/follow-ups", map[string]any{"leadId": aid, "type": "site_visit", "subject": "Venue tour",
		"dueAt": time.Now().Add(48 * time.Hour).Format(time.RFC3339)}).JSON()
	sx.Must(422, "POST", "/api/v1/crm/follow-ups", map[string]any{"leadId": aid, "type": "call", "subject": "No date"})
	if items := sx.Must(200, "GET", "/api/v1/crm/follow-ups", nil).Items(); !containsID(items, str(fu["id"])) {
		t.Fatal("follow-up missing from my list")
	}
	done := sx.Must(200, "POST", "/api/v1/crm/follow-ups/"+str(fu["id"])+":complete", map[string]any{"outcome": "Loved the garden"}).JSON()
	if done["status"] != "completed" {
		t.Fatalf("completed follow-up: %v", done)
	}
	fu2 := sx.Must(201, "POST", "/api/v1/crm/follow-ups", map[string]any{"leadId": aid, "type": "food_tasting", "subject": "Tasting",
		"dueAt": time.Now().Add(72 * time.Hour).Format(time.RFC3339)}).JSON()
	sx.Must(200, "POST", "/api/v1/crm/follow-ups/"+str(fu2["id"])+":cancel", map[string]any{"reason": "Moved to next month"})

	// Qualify and convert: customer + corporate account + opportunity in the Wedding pipeline.
	sx.Must(200, "POST", "/api/v1/crm/leads/"+aid+":qualify", map[string]any{"note": "Budget confirmed"})
	conv := sx.Must(200, "POST", "/api/v1/crm/leads/"+aid+":convert", map[string]any{"title": "Wedding Andi " + sfx}).JSON()
	opp := conv["opportunity"].(map[string]any)
	if conv["customerId"] == nil || conv["corporateAccountId"] == nil || opp["pipelineName"] != "Wedding" || str(opp["expectedValue"]) != "250000000" {
		t.Fatalf("conversion: %v", conv)
	}
	if l := sx.Must(200, "GET", "/api/v1/crm/leads/"+aid, nil).JSON(); l["status"] != "converted" || str(l["opportunityId"]) != str(opp["id"]) {
		t.Fatalf("converted lead: %v", l)
	}
	sx.Must(409, "POST", "/api/v1/crm/leads/"+aid+":qualify", map[string]any{})
	bsl := bs.Must(200, "POST", "/api/v1/crm/leads/"+str(b["id"])+":disqualify", map[string]any{"reason": "duplicate", "note": "Same family"}).JSON()
	if bsl["status"] != "unqualified" || bsl["unqualifiedReason"] != "duplicate" {
		t.Fatalf("disqualified: %v", bsl)
	}
	bs.Must(422, "POST", "/api/v1/crm/leads/"+str(b["id"])+":disqualify", map[string]any{"reason": "bored"})

	// Transfer the open work of the banquet sales to the sales executive.
	open := mk.Must(201, "POST", "/api/v1/crm/leads", map[string]any{"name": "Transfer " + sfx, "email": "tr" + sfx + "@lead.test", "source": "phone",
		"line": "banquet"}).JSON()
	sa.Must(200, "POST", "/api/v1/crm/leads/"+str(open["id"])+":assign", map[string]any{"userId": bsID})
	tr := sa.Must(200, "POST", "/api/v1/crm/leads:transfer", map[string]any{"fromUserId": bsID, "toUserId": sxID, "reason": "Resigned"}).JSON()
	if tr["leads"].(float64) < 1 {
		t.Fatalf("transfer: %v", tr)
	}
	if l := sa.Must(200, "GET", "/api/v1/crm/leads/"+str(open["id"]), nil).JSON(); str(l["ownerUserId"]) != sxID || l["assignmentMethod"] != "transfer" {
		t.Fatalf("transferred lead: %v", l)
	}
	sa.Must(422, "POST", "/api/v1/crm/leads:transfer", map[string]any{"fromUserId": sxID, "toUserId": sxID, "reason": "x"})

	// Website inquiry form (public) → lead with the line inferred.
	pub := anon(t, inst)
	ack := pub.Must(202, "POST", "/api/v1/public/inquiries", map[string]any{"propertyId": inst.Main, "guest": map[string]any{"name": "Web Bride " + sfx,
		"email": "bride" + sfx + "@web.test", "phone": "+62815" + sfx}, "message": "We plan our wedding reception for 400 guests", "consent": true,
		"channel": "utm:wedding-2026"}).JSON()
	if ack["status"] != "received" || ack["reference"] == nil {
		t.Fatalf("inquiry: %v", ack)
	}
	web := sa.Must(200, "GET", "/api/v1/crm/leads?q="+url.QueryEscape(str(ack["reference"])), nil).Items()
	if len(web) != 1 || web[0]["source"] != "website_form" || web[0]["line"] != "wedding" || web[0]["marketingConsent"] != true {
		t.Fatalf("inquiry lead: %v", web)
	}
	again := pub.Must(202, "POST", "/api/v1/public/inquiries", map[string]any{"propertyId": inst.Main, "guest": map[string]any{"name": "Web Bride",
		"email": "bride" + sfx + "@web.test"}, "message": "Is 12 December still available?"}).JSON()
	if again["reference"] != ack["reference"] {
		t.Fatalf("a second inquiry of the same contact is appended: %v vs %v", again, ack)
	}
	pub.Must(400, "POST", "/api/v1/public/inquiries", map[string]any{"guest": map[string]any{"name": "x", "email": "x@y.z"}, "message": "hi"})
	// Website contact form about a sales topic → lead too.
	pub.Must(202, "POST", "/api/v1/public/contact", map[string]any{"propertyId": inst.Main, "guest": map[string]any{"name": "Contact " + sfx,
		"email": "contact" + sfx + "@web.test", "phone": "+62816" + sfx}, "topic": "meeting", "message": "Meeting package for 50 people"})
	if items := sa.Must(200, "GET", "/api/v1/crm/leads?q=contact"+sfx, nil).Items(); len(items) != 1 || items[0]["line"] != "mice" {
		t.Fatalf("contact form lead: %v", items)
	}

	// Department mailbox → lead once per message; replies are appended.
	em := map[string]any{"from": "events" + sfx + "@corp.test", "fromName": "Corp Events", "to": "mice@club.test", "subject": "Annual meeting 120 pax",
		"text": "Please quote a full-day meeting for 120 people", "messageId": "<m1-" + sfx + "@corp.test>"}
	c1 := sa.Must(200, "POST", "/api/v1/crm/leads:capture-email", em).JSON()
	if c1["status"] != "created" {
		t.Fatalf("e-mail capture: %v", c1)
	}
	if c2 := sa.Must(200, "POST", "/api/v1/crm/leads:capture-email", em).JSON(); c2["status"] != "duplicate" {
		t.Fatalf("same message twice: %v", c2)
	}
	em["messageId"], em["text"] = "<m2-"+sfx+"@corp.test>", "Can we add a gala dinner?"
	if c3 := sa.Must(200, "POST", "/api/v1/crm/leads:capture-email", em).JSON(); c3["status"] != "appended" || c3["leadId"] != c1["leadId"] {
		t.Fatalf("reply appended: %v", c3)
	}
	if l := sa.Must(200, "GET", "/api/v1/crm/leads/"+str(c1["leadId"]), nil).JSON(); l["line"] != "mice" || l["source"] != "email" {
		t.Fatalf("e-mail lead: %v", l)
	}

	// WhatsApp inbound (signed BSP webhook) from an unknown number → lead.
	pa := platformAdmin(t, inst)
	wa := integrationID(t, inst, "mock-whatsapp")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+wa, map[string]any{"enabled": true})
	secret := str(pa.Must(200, "POST", "/api/v1/platform/integrations/"+wa+":rotate-webhook-secret", nil).JSON()["webhookSecret"])
	waPhone := "62817" + sfx
	body, _ := json.Marshal(map[string]any{"id": "wamid." + uuid.NewString(), "type": "message.received", "data": map[string]any{
		"messages": []map[string]any{{"id": "wamid.in." + sfx, "from": waPhone, "name": "WA Golfer " + sfx, "text": "Corporate golf day for 40 players?"}}}})
	hook := anon(t, inst)
	hook.Property = uuid.Nil
	hook.Must(200, "POST", "/api/v1/webhooks/mock-whatsapp", body, integration.SignatureHeader, integration.Sign(secret, body, time.Now()))
	var waLead map[string]any
	slsDispatch(t, "WhatsApp lead", func() bool {
		items := sa.Must(200, "GET", "/api/v1/crm/leads?filter[source]=whatsapp&q="+sfx, nil).Items()
		if len(items) == 1 {
			waLead = items[0]
		}
		return waLead != nil
	})
	if waLead["line"] != "golf" || waLead["phone"] != "+"+waPhone {
		t.Fatalf("WhatsApp lead: %v", waLead)
	}

	// Spreadsheet import: preview validates, commit creates leads with
	// opportunities, a re-import is idempotent by external reference.
	csv := "externalRef,name,phone,email,source,line,status,budget,opportunityTitle,expectedValue,stage,expectedCloseDate,createdAt\n" +
		"XL-" + sfx + "-1,Imported Bride,+62818" + sfx + ",,instagram,wedding,qualified,300000000,Wedding XL,300000000,FOOD_TASTING," +
		time.Now().AddDate(0, 2, 0).Format("2006-01-02") + "," + time.Now().AddDate(0, 0, -10).Format("2006-01-02") + "\n" +
		"XL-" + sfx + "-2,Imported Corp,,corp" + sfx + "@xl.test,phone,mice,new,,,,,,\n" +
		"XL-" + sfx + "-3,,,,,mice,new,,,,,,\n"
	pv := sa.Must(200, "POST", "/api/v1/crm/leads:import", map[string]any{"mode": "preview", "csv": csv}).JSON()
	if pv["totalRows"].(float64) != 3 || pv["failed"].(float64) != 1 || pv["created"].(float64) != 2 {
		t.Fatalf("import preview: %v", pv)
	}
	if items := sa.Must(200, "GET", "/api/v1/crm/leads?q=Imported", nil).Items(); len(items) != 0 {
		t.Fatalf("a preview saves nothing: %v", items)
	}
	cm := sa.Must(200, "POST", "/api/v1/crm/leads:import", map[string]any{"mode": "commit", "csv": csv}).JSON()
	if cm["created"].(float64) != 2 || cm["opportunities"].(float64) != 1 {
		t.Fatalf("import commit: %v", cm)
	}
	if re := sa.Must(200, "POST", "/api/v1/crm/leads:import", map[string]any{"mode": "commit", "csv": csv}).JSON(); re["created"].(float64) != 0 ||
		re["existing"].(float64) != 2 {
		t.Fatalf("re-import: %v", re)
	}
	sx.Must(403, "POST", "/api/v1/crm/leads:import", map[string]any{"mode": "preview", "csv": csv})
}

// EP-02/03/04 acceptance: opportunity on the Corporate Golf pipeline with
// stage moves, activities, board and forecast; a quotation priced with tax
// & service and payment terms, a discount above the Sales Policies limit
// approved through the approval engine, sent with PDF and secure link,
// revised to version 2 and accepted by the customer on the public link
// (name, terms, IP); the opportunity is won, the payment schedule is issued
// once (FR-QUO-06/07), the deal paid in full earns the commission of the
// scheme, statements are approved, paid and exported, and the target
// achievement, reports and CRM Performance dashboard show the deal.
func TestP3SalesQuotationToCommission(t *testing.T) {
	sa := superAdmin(t, inst)
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	sx := roleUser(t, inst, "sales_executive")
	sxID := slsUserID(t, "role.sales_executive@matrix.test")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	loc := clubLoc(inst)
	today := time.Now().In(loc)
	pipe, stages := slsPipeline(t, sa, "CORP-GOLF")

	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "SLC" + sfx, "name": "PT Golf Partner " + sfx,
		"email": "golf" + sfx + "@partner.test"}))
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "SLP" + sfx, "name": "Budi Partner " + sfx,
		"email": "budi" + sfx + "@partner.test", "phone": "+62819" + sfx}))
	venue := resourceOf(t, sa, "SLSV"+sfx, "meeting_room", intp(80), nil)

	// Commission scheme for the sales: 5% of the net of golf deals.
	sa.Must(201, "POST", "/api/v1/crm/commission-schemes", map[string]any{"code": "SLS" + sfx, "name": "Golf 5% " + sfx, "schemeType": "percent",
		"basis": "net", "rates": []map[string]any{{"line": "golf", "percent": "5"}}, "userId": sxID, "effectiveFrom": today.AddDate(0, 0, -1).Format("2006-01-02"),
		"clawbackDays": 90})
	sa.Must(422, "POST", "/api/v1/crm/commission-schemes", map[string]any{"code": "SLX" + sfx, "name": "Bad", "rates": []map[string]any{{"line": "golf",
		"percent": "150"}}, "effectiveFrom": today.Format("2006-01-02")})
	sa.Must(201, "POST", "/api/v1/crm/sales-targets", map[string]any{"userId": sxID, "line": "golf", "periodType": "month",
		"periodStart": time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02"),
		"periodEnd":   time.Date(today.Year(), today.Month()+1, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), "targetRevenue": "100000000", "targetDeals": 2})

	// Opportunity, stage moves, activity, board, forecast, venue availability.
	o := sx.Must(201, "POST", "/api/v1/crm/opportunities", map[string]any{"title": "Golf Day " + sfx, "line": "golf", "customerId": cust,
		"corporateAccountId": corp, "expectedValue": "60000000", "expectedCloseDate": today.AddDate(0, 0, 20).Format("2006-01-02"),
		"eventDate": today.AddDate(0, 1, 0).Format("2006-01-02"), "pax": 40, "venueResourceId": venue}).JSON()
	oid := str(o["id"])
	if str(o["pipelineId"]) != pipe || str(o["stageId"]) != stages["INQUIRY"] || str(o["ownerUserId"]) != sxID || str(o["probability"]) != "10" {
		t.Fatalf("new opportunity: %v", o)
	}
	o = sx.Must(200, "POST", "/api/v1/crm/opportunities/"+oid+":move-stage", map[string]any{"stageId": stages["PROPOSAL"], "note": "Sent rate card"}).JSON()
	if str(o["probability"]) != "40" || len(o["stageHistory"].([]any)) < 2 {
		t.Fatalf("moved: %v", o)
	}
	sx.Must(422, "POST", "/api/v1/crm/opportunities/"+oid+":move-stage", map[string]any{"stageId": stages["WON"]})
	sx.Must(200, "PATCH", "/api/v1/crm/opportunities/"+oid, map[string]any{"notes": "Shotgun start", "pax": 44})
	sx.Must(201, "POST", "/api/v1/crm/opportunities/"+oid+"/activities", map[string]any{"type": "meeting", "subject": "Course walk"})
	board := sx.Must(200, "GET", "/api/v1/crm/pipelines/"+pipe+"/board?mine=true", nil).JSON()
	found := false
	for _, s := range board["stages"].([]any) {
		st := s.(map[string]any)
		if str(st["stageId"]) == stages["PROPOSAL"] && containsID(toMaps(st["opportunities"]), oid) {
			found = true
		}
	}
	if !found {
		t.Fatalf("board misses the opportunity: %v", board)
	}
	if fc := sx.Must(200, "GET", "/api/v1/crm/sales-forecast", nil).JSON(); bilDec(fc["weightedValue"]).IsZero() {
		t.Fatalf("forecast: %v", fc)
	}
	va := sx.Must(200, "GET", "/api/v1/crm/opportunities/"+oid+"/venue-availability", nil).Items()
	if len(va) != 1 || str(va[0]["resourceId"]) != venue || va[0]["available"] != true {
		t.Fatalf("venue availability: %v", va)
	}
	if list := sx.Must(200, "GET", "/api/v1/crm/opportunities?mine=true&filter[line]=golf", nil).Items(); !containsID(list, oid) {
		t.Fatal("my golf opportunities miss the new one")
	}
	sx.Must(409, "POST", "/api/v1/crm/opportunities/"+oid+":win", map[string]any{})

	// Discount above the limit needs the GM's approval (workflow on the document type).
	sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "sales_quotation_discount", "name": "Discount " + sfx,
		"steps": []map[string]any{{"stepNo": 1, "name": "General Manager", "approverType": "role", "approverRoleId": roleID(t, sa, "general_manager")}}})
	qin := map[string]any{"opportunityId": oid, "pricingMode": "nett", "discountPercent": "15",
		"paymentTerms": []map[string]any{{"label": "Down Payment 40%", "percent": "40", "dueDays": 3}, {"label": "Final Payment", "percent": "60", "dueDays": 10}},
		"lines": []map[string]any{{"itemType": "service", "description": "Green fee weekday", "quantity": "40", "unitPrice": "1250000"},
			{"itemType": "venue", "itemRef": venue, "description": "Meeting room awarding", "quantity": "1", "unitPrice": "10000000"}}}
	q1 := sx.Must(201, "POST", "/api/v1/crm/quotations", qin, "Idempotency-Key", newKey()).JSON()
	q1id := str(q1["id"])
	if q1["status"] != "draft" || q1["approvalStatus"] != "required" || !bilDec(q1["subtotal"]).Equal(decimal.NewFromInt(60_000_000)) ||
		!bilDec(q1["discount"]).Equal(decimal.NewFromInt(9_000_000)) || !bilDec(q1["total"]).Equal(decimal.NewFromInt(51_000_000)) {
		t.Fatalf("draft quotation: %v", q1)
	}
	if n := len(q1["paymentTerms"].([]any)); n != 2 {
		t.Fatalf("payment terms: %v", q1["paymentTerms"])
	}
	sx.Must(409, "POST", "/api/v1/crm/quotations/"+q1id+":send", map[string]any{})
	q1 = sx.Must(200, "POST", "/api/v1/crm/quotations/"+q1id+":submit-approval", nil).JSON()
	if q1["status"] != "pending_approval" || q1["approvalStatus"] != "pending" {
		t.Fatalf("submitted: %v", q1)
	}
	gm.Must(200, "POST", "/api/v1/platform/approvals/"+str(q1["approvalRequestId"])+":approve", map[string]any{"reason": "Strategic account"})
	q1 = sx.Must(200, "GET", "/api/v1/crm/quotations/"+q1id, nil).JSON()
	if q1["status"] != "draft" || q1["approvalStatus"] != "approved" {
		t.Fatalf("approved discount: %v", q1)
	}
	sent := sx.Must(200, "POST", "/api/v1/crm/quotations/"+q1id+":send", map[string]any{"channels": []string{"email"}, "message": "As discussed"}).JSON()
	if sent["status"] != "sent" || sent["publicLink"] == nil {
		t.Fatalf("sent: %v", sent)
	}
	if r := sx.Must(200, "GET", "/api/v1/crm/quotations/"+q1id+"/pdf", nil); len(r.Body) < 100 || string(r.Body[:4]) != "%PDF" {
		t.Fatalf("quotation pdf: %d bytes", len(r.Body))
	}
	pub := anon(t, inst)
	tok1 := slsToken(t, sent)
	if v := pub.Must(200, "GET", "/api/v1/public/quotations/"+tok1, nil).JSON(); v["number"] != sent["number"] || v["status"] != "sent" ||
		len(v["lines"].([]any)) != 2 {
		t.Fatalf("public quotation: %v", v)
	}
	pub.Must(404, "GET", "/api/v1/public/quotations/not-a-token", nil)

	// Revise: version 2 (edited lines); v1 becomes Revised and its link can no longer be accepted.
	q2 := sx.Must(200, "POST", "/api/v1/crm/quotations/"+q1id+":revise", nil).JSON()
	q2id := str(q2["id"])
	if q2["version"].(float64) != 2 || q2["status"] != "draft" || len(q2["versions"].([]any)) != 2 {
		t.Fatalf("revised: %v", q2)
	}
	qin["discountPercent"] = "10"
	qin["lines"] = []map[string]any{{"itemType": "service", "description": "Green fee weekday", "quantity": "44", "unitPrice": "1250000"},
		{"itemType": "venue", "itemRef": venue, "description": "Meeting room awarding", "quantity": "1", "unitPrice": "10000000"}}
	q2 = sx.Must(200, "PUT", "/api/v1/crm/quotations/"+q2id, qin).JSON()
	if q2["approvalStatus"] != "not_required" || !bilDec(q2["total"]).Equal(decimal.NewFromInt(58_500_000)) {
		t.Fatalf("edited v2: %v", q2)
	}
	if v := pub.Must(200, "GET", "/api/v1/public/quotations/"+tok1, nil).JSON(); v["status"] != "revised" {
		t.Fatalf("the old link shows the quotation was revised: %v", v)
	}
	pub.Must(409, "POST", "/api/v1/public/quotations/"+tok1+":accept", map[string]any{"name": "Budi", "termsAccepted": true})
	q2 = sx.Must(200, "POST", "/api/v1/crm/quotations/"+q2id+":send", map[string]any{}).JSON()
	tok2 := slsToken(t, q2)
	pub.Must(422, "POST", "/api/v1/public/quotations/"+tok2+":accept", map[string]any{"name": "Budi"})
	acc := slsAcceptPublic(t, pub, tok2, map[string]any{"name": "Budi Partner", "termsAccepted": true})
	if acc["status"] != "accepted" || acc["acceptedByName"] != "Budi Partner" {
		t.Fatalf("public acceptance: %v", acc)
	}
	pub.Must(409, "POST", "/api/v1/public/quotations/"+tok2+":accept", map[string]any{"name": "Budi Partner", "termsAccepted": true})
	q2 = sx.Must(200, "GET", "/api/v1/crm/quotations/"+q2id, nil).JSON()
	dec := q2["decision"].(map[string]any)
	if q2["acceptedVia"] != "public_link" || dec["ip"] == nil || dec["termsAccepted"] != true {
		t.Fatalf("acceptance record: %v", q2)
	}
	if o := sx.Must(200, "GET", "/api/v1/crm/opportunities/"+oid, nil).JSON(); o["status"] != "won" || str(o["wonQuotationId"]) != q2id {
		t.Fatalf("won opportunity: %v", o)
	}
	if items := sx.Must(200, "GET", "/api/v1/crm/quotations?filter[opportunityId]="+oid+"&allVersions=true", nil).Items(); len(items) != 2 {
		t.Fatalf("all versions: %v", items)
	}

	// crm.quotation_accepted → the payment schedule of the deal, once.
	var sched map[string]any
	slsDispatch(t, "quotation payment schedule", func() bool {
		items := fin.Must(200, "GET", "/api/v1/billing/payment-schedules?filter[sourceType]=quotation&limit=200", nil).Items()
		for _, s := range items {
			if s["sourceRef"] == q2["number"] {
				sched = s
			}
		}
		return sched != nil
	})
	sd := fin.Must(200, "GET", "/api/v1/billing/payment-schedules/"+str(sched["id"]), nil).JSON()
	lines := sd["lines"].([]any)
	if len(lines) != 2 || !bilDec(sd["totalAmount"]).Equal(decimal.NewFromInt(58_500_000)) ||
		!bilDec(lines[0].(map[string]any)["amount"]).Equal(decimal.NewFromInt(23_400_000)) {
		t.Fatalf("schedule of the deal: %v", sd)
	}
	var schedules int
	sysQueryRow(t, inst, `SELECT count(*) FROM billing.payment_schedules WHERE source_type = 'quotation' AND source_ref = $1`, []any{q2["number"]}, &schedules)
	if schedules != 1 {
		t.Fatalf("schedules of the quotation: %d", schedules)
	}

	// DP paid: not yet commission; final paid: commission 5% of the net.
	for i, l := range lines {
		ln := l.(map[string]any)
		fin.Must(201, "POST", "/api/v1/billing/payment-schedules/"+str(sched["id"])+"/lines/"+str(ln["id"])+":pay", map[string]any{"methodType": "bank_transfer",
			"reference": fmt.Sprintf("TRF-%s-%d", sfx, i)})
		if i == 0 {
			slsDispatch(t, "DP attributed", func() bool {
				return bilDec(sx.Must(200, "GET", "/api/v1/crm/quotations/"+q2id, nil).JSON()["paidAmount"]).Equal(decimal.NewFromInt(23_400_000))
			})
			if cl := fin.Must(200, "GET", "/api/v1/crm/commission-lines?filter[quotationId]="+q2id, nil).Items(); len(cl) != 0 {
				t.Fatalf("commission before the deal is paid: %v", cl)
			}
		}
	}
	var earned map[string]any
	slsDispatch(t, "commission earned", func() bool {
		cl := fin.Must(200, "GET", "/api/v1/crm/commission-lines?filter[quotationId]="+q2id, nil).Items()
		if len(cl) == 1 {
			earned = cl[0]
		}
		return earned != nil
	})
	if earned["kind"] != "earned" || str(earned["userId"]) != sxID || !bilDec(earned["amount"]).Equal(decimal.NewFromInt(2_925_000)) {
		t.Fatalf("earned commission (5%% of 58,500,000): %v", earned)
	}

	// Manual adjustment, statement generated, approved (no workflow), paid, exported.
	period := today.Format("2006-01")
	fin.Must(201, "POST", "/api/v1/crm/commission-adjustments", map[string]any{"userId": sxID, "amount": "-25000", "reason": "Shared deal correction",
		"period": period})
	fin.Must(422, "POST", "/api/v1/crm/commission-adjustments", map[string]any{"userId": sxID, "amount": "10", "reason": ""})
	stmts := fin.Must(200, "POST", "/api/v1/crm/commission-statements:generate", map[string]any{"period": period}).Items()
	var st map[string]any
	for _, s := range stmts {
		if str(s["userId"]) == sxID {
			st = s
		}
	}
	if st == nil || !bilDec(st["total"]).Equal(decimal.NewFromInt(2_900_000)) || st["status"] != "draft" {
		t.Fatalf("statement of the sales: %v", stmts)
	}
	if d := fin.Must(200, "GET", "/api/v1/crm/commission-statements/"+str(st["id"]), nil).JSON(); len(d["lines"].([]any)) != 2 {
		t.Fatalf("statement lines: %v", d)
	}
	ap := fin.Must(200, "POST", "/api/v1/crm/commission-statements/"+str(st["id"])+":submit", nil).JSON()
	if ap["status"] != "approved" {
		t.Fatalf("statement without a workflow is approved at once: %v", ap)
	}
	fin.Must(409, "POST", "/api/v1/crm/commission-statements/"+str(st["id"])+":submit", nil)
	pd := fin.Must(200, "POST", "/api/v1/crm/commission-statements/"+str(st["id"])+":mark-paid", map[string]any{"reference": "PAYROLL-" + sfx}).JSON()
	if pd["status"] != "paid" {
		t.Fatalf("paid statement: %v", pd)
	}
	if list := fin.Must(200, "GET", "/api/v1/crm/commission-statements?filter[period]="+period+"&filter[status]=paid", nil).Items(); !containsID(list, str(st["id"])) {
		t.Fatal("paid statement missing from the list")
	}
	csvOut := fin.Must(200, "GET", "/api/v1/crm/commission-statements:export?period="+period, nil)
	if !strings.Contains(string(csvOut.Body), str(st["number"])) || !strings.Contains(csvOut.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("payroll export: %s", csvOut.Body)
	}
	sx.Must(403, "POST", "/api/v1/crm/commission-statements:generate", map[string]any{"period": period})

	// Target achievement counts the paid deal.
	ach := sx.Must(200, "GET", "/api/v1/crm/sales-target-achievements?userId="+sxID, nil).Items()
	if len(ach) == 0 || bilDec(ach[0]["achievedRevenue"]).IsZero() || ach[0]["achievedDeals"].(float64) < 1 {
		t.Fatalf("target achievement: %v", ach)
	}

	// Reports and the CRM Performance dashboard.
	from, to := today.AddDate(0, 0, -1).Format("2006-01-02"), today.AddDate(0, 0, 1).Format("2006-01-02")
	for _, code := range []string{"crm.lead_source", "crm.sales_pipeline", "crm.quotation", "crm.sales_commission"} {
		res := gm.Must(200, "GET", "/api/v1/reporting/reports/"+code+"?params[from]="+from+"&params[to]="+to, nil).JSON()
		if res["rows"] == nil {
			t.Fatalf("%s: %v", code, res)
		}
	}
	qr := gm.Must(200, "GET", "/api/v1/reporting/reports/crm.quotation?params[from]="+from+"&params[to]="+to+"&params[status]=accepted", nil).JSON()
	if !strings.Contains(fmt.Sprint(qr["rows"]), str(q2["number"])) {
		t.Fatalf("quotation report misses the accepted deal: %v", qr["rows"])
	}
	dash := gm.Must(200, "GET", "/api/v1/reporting/dashboards/crm-performance?from="+from+"&to="+to, nil).JSON()
	kpis := map[string]string{}
	for _, k := range dash["kpis"].([]any) {
		km := k.(map[string]any)
		kpis[str(km["key"])] = str(km["value"])
	}
	if d, _ := decimal.NewFromString(kpis["deals_won"]); d.LessThan(decimal.NewFromInt(58_500_000)) {
		t.Fatalf("CRM Performance: %v", kpis)
	}
}

// FR-QUO-05/06, FR-PIPE-04: rejection on the public link and by staff,
// lost and reopened opportunities, and the membership deal — the accepted
// quotation becomes a draft Membership Application (once); its activation
// pays the deal and earns the commission at the policy rate.
func TestP3SalesConversions(t *testing.T) {
	sa := superAdmin(t, inst)
	sx := roleUser(t, inst, "sales_executive")
	sxID := slsUserID(t, "role.sales_executive@matrix.test")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "SLM" + sfx, "name": "Maya Applicant " + sfx,
		"email": "maya" + sfx + "@member.test", "phone": "+62821" + sfx, "birthDate": "1985-03-01", "gender": "female"}))

	// Lost with a reason, reopened.
	o := sx.Must(201, "POST", "/api/v1/crm/opportunities", map[string]any{"title": "Lost deal " + sfx, "line": "other", "customerId": cust,
		"expectedValue": "1000000"}).JSON()
	sx.Must(422, "POST", "/api/v1/crm/opportunities/"+str(o["id"])+":lose", map[string]any{"reason": "weather"})
	lost := sx.Must(200, "POST", "/api/v1/crm/opportunities/"+str(o["id"])+":lose", map[string]any{"reason": "competitor", "note": "Club X"}).JSON()
	if lost["status"] != "lost" || lost["lostReason"] != "competitor" {
		t.Fatalf("lost: %v", lost)
	}
	re := sx.Must(200, "POST", "/api/v1/crm/opportunities/"+str(o["id"])+":reopen", map[string]any{"note": "Came back"}).JSON()
	if re["status"] != "open" {
		t.Fatalf("reopened: %v", re)
	}
	// Won with a quotation accepted directly for the customer (linked to the opportunity).
	dq := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "title": "Direct deal " + sfx, "line": "other",
		"lines": []map[string]any{{"itemType": "service", "description": "Direct", "quantity": "1", "unitPrice": "1500000"}}}, "Idempotency-Key", newKey()).JSON()
	sx.Must(200, "POST", "/api/v1/crm/quotations/"+str(dq["id"])+":accept", map[string]any{"note": "Verbal"})
	won := sx.Must(200, "POST", "/api/v1/crm/opportunities/"+str(o["id"])+":win", map[string]any{"quotationId": dq["id"], "note": "Signed"}).JSON()
	if won["status"] != "won" || str(won["wonQuotationId"]) != str(dq["id"]) || !bilDec(won["expectedValue"]).Equal(decimal.NewFromInt(1_500_000)) {
		t.Fatalf("won: %v", won)
	}
	sx.Must(409, "POST", "/api/v1/crm/opportunities/"+str(o["id"])+":win", map[string]any{})
	if q := sx.Must(200, "GET", "/api/v1/crm/quotations/"+str(dq["id"]), nil).JSON(); str(q["opportunityId"]) != str(o["id"]) {
		t.Fatalf("the quotation is linked to the won opportunity: %v", q)
	}
	o = sx.Must(201, "POST", "/api/v1/crm/opportunities", map[string]any{"title": "Second deal " + sfx, "line": "other", "customerId": cust,
		"expectedValue": "1000000"}).JSON()

	// Rejection on the public link (reason required) and by staff.
	// Free-text items need crm.quotation.free_item (FR-QUO-01).
	free := []map[string]any{{"itemType": "other", "description": "Fireworks show", "quantity": "1", "unitPrice": "5000000"}}
	sx.Must(403, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "title": "Free " + sfx, "lines": free}, "Idempotency-Key", newKey())
	fq := sa.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "title": "Free " + sfx, "lines": free,
		"ownerUserId": sxID}, "Idempotency-Key", newKey()).JSON()
	sx.Must(200, "PUT", "/api/v1/crm/quotations/"+str(fq["id"]), map[string]any{"title": "Free item kept " + sfx, "lines": free})
	line := []map[string]any{{"itemType": "service", "description": "Private event", "quantity": "1", "unitPrice": "5000000"}}
	r1 := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"opportunityId": o["id"], "lines": line}, "Idempotency-Key", newKey()).JSON()
	r1 = sx.Must(200, "POST", "/api/v1/crm/quotations/"+str(r1["id"])+":send", map[string]any{}).JSON()
	pub := anon(t, inst)
	tok := slsToken(t, r1)
	pub.Must(422, "POST", "/api/v1/public/quotations/"+tok+":reject", map[string]any{})
	if rj := pub.Must(200, "POST", "/api/v1/public/quotations/"+tok+":reject", map[string]any{"reason": "Over budget"}).JSON(); rj["status"] != "rejected" {
		t.Fatalf("public rejection: %v", rj)
	}
	sx.Must(409, "POST", "/api/v1/crm/quotations/"+str(r1["id"])+":accept", map[string]any{})
	r2 := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "title": "Second try " + sfx, "lines": line},
		"Idempotency-Key", newKey()).JSON()
	sx.Must(409, "POST", "/api/v1/crm/quotations/"+str(r2["id"])+":reject", map[string]any{"reason": "draft"})
	sx.Must(200, "POST", "/api/v1/crm/quotations/"+str(r2["id"])+":send", map[string]any{"channels": []string{"email"}})
	sx.Must(422, "POST", "/api/v1/crm/quotations/"+str(r2["id"])+":reject", map[string]any{})
	if rj := sx.Must(200, "POST", "/api/v1/crm/quotations/"+str(r2["id"])+":reject", map[string]any{"reason": "Chose a weekday"}).JSON(); rj["status"] != "rejected" {
		t.Fatalf("staff rejection: %v", rj)
	}

	// Membership deal: staff acceptance (signed copy) → draft application once.
	mq := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "title": "Individual membership " + sfx, "line": "membership",
		"packageRef": "IND-1Y", "pricingMode": "nett", "lines": []map[string]any{{"itemType": "package", "itemRef": "IND-1Y",
			"description": "Golf Individual — 1 Year", "quantity": "1", "unitPrice": "62000000"}}}, "Idempotency-Key", newKey()).JSON()
	acc := sx.Must(200, "POST", "/api/v1/crm/quotations/"+str(mq["id"])+":accept", map[string]any{"acceptedByName": "Maya Applicant", "note": "Signed copy"}).JSON()
	if acc["status"] != "accepted" || acc["acceptedVia"] != "staff" || acc["opportunityId"] != nil {
		t.Fatalf("staff acceptance: %v", acc)
	}
	var app map[string]any
	slsDispatch(t, "membership application", func() bool {
		items := sa.Must(200, "GET", "/api/v1/membership/applications?filter[customerId]="+cust, nil).Items()
		if len(items) == 1 {
			app = items[0]
		}
		return app != nil
	})
	if app["status"] != "draft" || !strings.Contains(str(app["notes"]), str(mq["number"])) {
		t.Fatalf("application from the quotation: %v", app)
	}
	var schedules int
	sysQueryRow(t, inst, `SELECT count(*) FROM billing.payment_schedules WHERE source_type = 'quotation' AND source_ref = $1`, []any{mq["number"]}, &schedules)
	if schedules != 0 {
		t.Fatalf("a membership deal is billed by its application, not a quotation schedule: %d", schedules)
	}
	// The application runs its P1 flow; activation (fee paid) pays the deal.
	appID := str(app["id"])
	sub := sa.Must(200, "POST", "/api/v1/membership/applications/"+appID+":submit", nil).JSON()
	if sub["status"] == "pending" && sub["approvalRequestId"] != nil {
		login(t, inst, "membership@demo.oneclub.id", demoPassword).Must(200, "POST", "/api/v1/platform/approvals/"+str(sub["approvalRequestId"])+":approve",
			map[string]any{})
		sub = sa.Must(200, "GET", "/api/v1/membership/applications/"+appID, nil).JSON()
	}
	if fid := sub["feeFolioId"]; fid != nil {
		bal := sa.Must(200, "GET", "/api/v1/billing/folios/"+str(fid), nil).JSON()["summary"].(map[string]any)["balance"]
		sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": fid, "methodType": "cash", "amount": str(bal)})
	}
	var line0 map[string]any
	slsDispatch(t, "membership commission", func() bool {
		cl := sa.Must(200, "GET", "/api/v1/crm/commission-lines?filter[quotationId]="+str(mq["id"]), nil).Items()
		if len(cl) == 1 {
			line0 = cl[0]
		}
		return line0 != nil
	})
	// Default Sales Policies rate of the membership line: 3% of 62,000,000.
	if str(line0["userId"]) != sxID || !bilDec(line0["amount"]).Equal(decimal.NewFromInt(1_860_000)) {
		t.Fatalf("membership commission: %v", line0)
	}
	if q := sx.Must(200, "GET", "/api/v1/crm/quotations/"+str(mq["id"]), nil).JSON(); q["paidAt"] == nil {
		t.Fatalf("membership deal paid: %v", q)
	}
}

// FR-LEAD-06 first-response reminder and escalation, follow-up reminders,
// quotation expiry (crm.quotation_expired) and lead retention (UU PDP,
// PRD P3 §16 #19) run by the CRM Sales jobs.
func TestP3SalesJobs(t *testing.T) {
	sa := superAdmin(t, inst)
	sx := roleUser(t, inst, "sales_executive")
	sxID := slsUserID(t, "role.sales_executive@matrix.test")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	m := inst.App.Sales.Module

	soon := sa.Must(201, "POST", "/api/v1/crm/leads", map[string]any{"name": "Soon " + sfx, "phone": "+62831" + sfx, "source": "phone", "line": "golf",
		"ownerUserId": sxID}).JSON()
	late := sa.Must(201, "POST", "/api/v1/crm/leads", map[string]any{"name": "Late " + sfx, "phone": "+62832" + sfx, "source": "phone", "line": "golf",
		"ownerUserId": sxID}).JSON()
	sysExec(t, inst, `UPDATE crm.sales_leads SET first_response_due_at = now() + interval '10 minutes' WHERE id = $1`, soon["id"])
	sysExec(t, inst, `UPDATE crm.sales_leads SET first_response_due_at = now() - interval '5 minutes' WHERE id = $1`, late["id"])
	fu := sx.Must(201, "POST", "/api/v1/crm/follow-ups", map[string]any{"leadId": soon["id"], "type": "call", "subject": "Call back " + sfx,
		"dueAt": time.Now().Add(5 * time.Minute).Format(time.RFC3339)}).JSON()
	if _, err := m.RunSLA(t.Context()); err != nil {
		t.Fatal(err)
	}
	var reminded, escalated, followUp bool
	sysQueryRow(t, inst, `SELECT (SELECT sla_reminded_at IS NOT NULL FROM crm.sales_leads WHERE id = $1),
		(SELECT sla_escalated_at IS NOT NULL FROM crm.sales_leads WHERE id = $2), (SELECT reminded_at IS NOT NULL FROM crm.sales_activities WHERE id = $3)`,
		[]any{soon["id"], late["id"], fu["id"]}, &reminded, &escalated, &followUp)
	if !reminded || !escalated || !followUp {
		t.Fatalf("SLA reminder %v, escalation %v, follow-up reminder %v", reminded, escalated, followUp)
	}
	var notes int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications WHERE user_id = $1 AND event_code IN ('crm.sales_lead_sla_reminder',
		'crm.sales_lead_sla_escalated', 'crm.sales_follow_up_due')`, []any{sxID}, &notes)
	if notes < 3 {
		t.Fatalf("notifications to the sales: %d", notes)
	}
	if logs := sa.Must(200, "GET", "/api/v1/audit/logs?filter[action]=sla_escalated&filter[entityId]="+str(late["id"]), nil).Items(); len(logs) != 1 {
		t.Fatalf("escalation audit: %v", logs)
	}
	if _, err := m.RunSLA(t.Context()); err != nil {
		t.Fatal(err)
	}
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications WHERE user_id = $1 AND event_code = 'crm.sales_lead_sla_escalated'
		AND body LIKE '%' || $2 || '%'`, []any{sxID, late["number"]}, &notes)
	if notes != 1 {
		t.Fatalf("an escalation is sent once: %d", notes)
	}

	// Expiry: a sent quotation past its validity.
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "SLJ" + sfx, "name": "Expiring " + sfx, "email": "exp" + sfx + "@q.test"}))
	q := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "title": "Expiring " + sfx, "lines": []map[string]any{{"itemType": "service",
		"description": "Room", "quantity": "1", "unitPrice": "1000000"}}}, "Idempotency-Key", newKey()).JSON()
	q = sx.Must(200, "POST", "/api/v1/crm/quotations/"+str(q["id"])+":send", map[string]any{}).JSON()
	sysExec(t, inst, `UPDATE crm.sales_quotations SET valid_until = current_date - 1 WHERE id = $1`, q["id"])
	// Retention: a lead without customer and without activity for longer than the policy.
	old := sa.Must(201, "POST", "/api/v1/crm/leads", map[string]any{"name": "Old " + sfx, "email": "old" + sfx + "@lead.test", "source": "other"}).JSON()
	sysExec(t, inst, `UPDATE crm.sales_leads SET last_activity_at = now() - interval '800 days' WHERE id = $1`, old["id"])
	if _, err := m.RunDaily(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e := sx.Must(200, "GET", "/api/v1/crm/quotations/"+str(q["id"]), nil).JSON(); e["status"] != "expired" {
		t.Fatalf("expired quotation: %v", e)
	}
	anon(t, inst).Must(409, "POST", "/api/v1/public/quotations/"+slsToken(t, q)+":accept", map[string]any{"name": "Late", "termsAccepted": true})
	var events int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'crm.quotation_expired' AND aggregate_id = $1`, []any{q["id"]}, &events)
	if events != 1 {
		t.Fatalf("crm.quotation_expired events: %d", events)
	}
	if l := sa.Must(200, "GET", "/api/v1/crm/leads/"+str(old["id"]), nil).JSON(); l["name"] != "Anonymised lead" || l["email"] != nil {
		t.Fatalf("anonymised lead: %v", l)
	}
	if items := sa.Must(200, "GET", "/api/v1/crm/leads?q=old"+sfx, nil).Items(); len(items) != 0 {
		t.Fatalf("anonymised leads leave the lists: %v", items)
	}
}

func toMaps(v any) []map[string]any {
	var out []map[string]any
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}
