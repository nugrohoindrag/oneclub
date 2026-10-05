package e2e

// PRD P3 gap fixes, package C (money): quotation pricing from the catalogue
// with promotions and the per-line PDF, vouchers issued for campaigns,
// loyalty rewards and complaint compensations, the corporate AR migration,
// the §16 defaults, the loyalty segment multiplier and the acceptance
// tests of commission schemes, clawback, promotions, campaigns, invoices
// and the pipeline forecast.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"github.com/xuri/excelize/v2"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/integration"
)

// fixPolicy changes a club policy for the test (the value in force, else
// the default, with mutate applied) and restores the previous value at the
// end, so later tests see the policy unchanged.
func fixPolicy(t *testing.T, c *Client, code string, mutate func(v map[string]any)) {
	t.Helper()
	var entry map[string]any
	for _, e := range c.Must(200, "GET", "/api/v1/platform/club-policies/catalog", nil).Items() {
		if e["code"] == code {
			entry = e
		}
	}
	if entry == nil {
		t.Fatalf("policy %s not in the catalogue", code)
	}
	cur := entry["inForce"]
	if cur == nil {
		cur = entry["default"]
	}
	raw, _ := json.Marshal(cur)
	var before, after map[string]any
	_ = json.Unmarshal(raw, &before)
	_ = json.Unmarshal(raw, &after)
	mutate(after)
	cat := str(entry["category"])
	c.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": cat, "code": code, "name": code, "value": after})
	t.Cleanup(func() {
		c.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": cat, "code": code, "name": code, "value": before})
	})
}

// fixPNG is a small PNG logo.
func fixPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 60, 30))
	for x := 0; x < 60; x++ {
		for y := 0; y < 30; y++ {
			img.Set(x, y, color.RGBA{R: 20, G: 90, B: 60, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// fixLine returns the quotation line with a description.
func fixLine(t *testing.T, q map[string]any, desc string) map[string]any {
	t.Helper()
	for _, l := range q["lines"].([]any) {
		if m := l.(map[string]any); m["description"] == desc {
			return m
		}
	}
	t.Fatalf("line %q missing: %v", desc, q["lines"])
	return nil
}

// FR-QUO-01 / FR-PRM-06 / FR-QUO-08 / §16 #5 #11: quotation lines priced
// from the catalogue — banquet package per pax with its minimum pax,
// banquet menu per pax and a published Commercial package — with the
// active back-office promotion applied to the package line (not counted as
// a sales discount) unless the Sales Policies switch promotions off; the
// discount limits per role; the wedding template with the logo, the
// promotion and the Banquet Policies terms (DP 30%, 40% at H-60).
func TestP3FixMoneyQuotation(t *testing.T) {
	sa := superAdmin(t, inst)
	sx := roleUser(t, inst, "sales_executive")
	bm := roleUser(t, inst, "banquet_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	today := time.Now().In(clubLoc(inst))
	event := today.AddDate(0, 0, 120).Format("2006-01-02")
	cust := customer(t, sa, "FQC"+sfx, "Bride Fix "+sfx, map[string]any{"email": "bride" + sfx + "@fix.test", "phone": "+62852" + sfx})

	// Catalogue: banquet package (per pax, minimum 50), banquet menu, Commercial package.
	sa.Must(201, "POST", "/api/v1/banquet/packages", map[string]any{"code": "FQB" + sfx, "name": "Garden Wedding " + sfx, "category": "wedding",
		"pricingMethod": "per_pax", "price": "500000", "pricingMode": "nett", "minPax": 50})
	sa.Must(201, "POST", "/api/v1/banquet/menus", map[string]any{"code": "FQM" + sfx, "name": "Dessert buffet " + sfx, "menuType": "buffet",
		"pricePerPax": "150000", "pricingMode": "nett"})
	pkg := idOf(sa.Must(201, "POST", "/api/v1/commercial/packages", map[string]any{"code": "FQP" + sfx, "name": "Bridal golf " + sfx,
		"packageType": "golf_day", "pricingMode": "per_pax", "price": "750000", "minPax": 2, "taxMode": "nett", "channels": []string{"back_office", "quotation"}}))
	sa.Must(201, "POST", "/api/v1/commercial/package-components", map[string]any{"packageId": pkg, "componentType": "service", "name": "Golf clinic",
		"standalonePrice": "750000", "perPax": true, "revenueComponent": "golf_other", "businessLine": "golf"})
	sa.Must(200, "POST", "/api/v1/commercial/packages/"+pkg+":publish", nil)
	promo := idOf(sa.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "FQPR" + sfx, "name": "Bridal golf 10% " + sfx,
		"promoType": "percent_discount", "discountPercent": "10", "serviceTypes": []string{"package"}, "itemRefs": []string{"FQP" + sfx},
		"channels": []string{"back_office"}}))
	sa.Must(200, "POST", "/api/v1/commercial/promotions/"+promo+":activate", nil)

	// Logo of the letterhead (organization branding).
	body, ctype := multipartBody(t, nil, "file", "logo.png", string(fixPNG(t)))
	logo := sa.Must(201, "POST", "/api/v1/platform/files", body, "Content-Type", ctype).JSON()
	sa.Must(200, "PATCH", "/api/v1/platform/organization", map[string]any{"logoFileId": logo["id"]})

	lines := []map[string]any{
		{"itemType": "banquet_package", "itemRef": "FQB" + sfx, "description": "Wedding package", "quantity": "40"},
		{"itemType": "banquet_menu", "itemRef": "fqm" + sfx, "description": "Dessert buffet", "quantity": "40"},
		{"itemType": "package", "itemRef": "FQP" + sfx, "description": "Bridal golf", "quantity": "3"},
	}
	q := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "line": "wedding", "title": "Wedding " + sfx,
		"eventDate": event, "pax": 40, "lines": lines}, "Idempotency-Key", newKey()).JSON()
	wp, menu, golf := fixLine(t, q, "Wedding package"), fixLine(t, q, "Dessert buffet"), fixLine(t, q, "Bridal golf")
	if wp["priceSource"] != "banquet_package" || !dec(wp["unitPrice"]).Equal(decimal.NewFromInt(500000)) || !dec(wp["quantity"]).Equal(decimal.NewFromInt(50)) ||
		!dec(wp["total"]).Equal(decimal.NewFromInt(25_000_000)) {
		t.Fatalf("banquet package per pax with the 50 pax minimum: %v", wp)
	}
	if menu["priceSource"] != "banquet_menu" || !dec(menu["total"]).Equal(decimal.NewFromInt(6_000_000)) {
		t.Fatalf("banquet menu per pax: %v", menu)
	}
	var snap map[string]any
	_ = json.Unmarshal([]byte(fixJSON(golf["pricing"])), &snap)
	if golf["priceSource"] != "package" || !dec(golf["total"]).Equal(decimal.NewFromInt(2_025_000)) || !dec(golf["discount"]).Equal(decimal.NewFromInt(225_000)) ||
		!dec(snap["promotionDiscount"]).Equal(decimal.NewFromInt(225_000)) || !strings.Contains(fixJSON(snap["promotions"]), "FQPR"+sfx) ||
		snap["package"] != "FQP"+sfx {
		t.Fatalf("package line with the promotion in its snapshot: %v", golf)
	}
	if !dec(q["total"]).Equal(decimal.NewFromInt(33_025_000)) || !dec(q["discount"]).Equal(decimal.NewFromInt(225_000)) ||
		q["approvalStatus"] != "not_required" || !dec(q["discountPercent"]).IsZero() {
		t.Fatalf("an automatic promotion is no sales discount: %v", q)
	}
	terms := str(q["terms"])
	if !strings.Contains(terms, "down payment of 30%") || !strings.Contains(terms, "second payment of 40% is due 60 days before") ||
		!strings.Contains(terms, "Termin kedua 40% jatuh tempo H-60") || !strings.Contains(terms, "Corkage Rp 150.000") {
		t.Fatalf("Banquet Policies terms of a wedding quotation: %q", terms)
	}
	// The edit keeps the promotion of the stored line.
	q = sx.Must(200, "PUT", "/api/v1/crm/quotations/"+str(q["id"]), map[string]any{"notes": "Garden ceremony"}).JSON()
	if !dec(q["total"]).Equal(decimal.NewFromInt(33_025_000)) || !dec(fixLine(t, q, "Bridal golf")["discount"]).Equal(decimal.NewFromInt(225_000)) {
		t.Fatalf("edited quotation keeps the promotion: %v", q)
	}
	pdf := sx.Must(200, "GET", "/api/v1/crm/quotations/"+str(q["id"])+"/pdf", nil).Body
	for _, want := range []string{"Wedding Quotation / Penawaran Pernikahan", "/Subtype /Image", "Promotion / Promosi: Bridal golf 10% " + sfx,
		"Event Date / Tanggal Acara", "1. Uang muka 30%"} {
		if !bytes.Contains(pdf, []byte(want)) {
			t.Fatalf("quotation PDF misses %q", want)
		}
	}
	// A golf quotation keeps the Sales Policies terms and its own template.
	g := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "line": "golf", "title": "Golf " + sfx,
		"lines": []map[string]any{{"itemType": "service", "description": "Green fee", "quantity": "2", "unitPrice": "1000000"}}},
		"Idempotency-Key", newKey()).JSON()
	if strings.Contains(str(g["terms"]), "Corkage") {
		t.Fatalf("golf quotation with banquet terms: %v", g["terms"])
	}
	if pdf := sx.Must(200, "GET", "/api/v1/crm/quotations/"+str(g["id"])+"/pdf", nil).Body; !bytes.Contains(pdf, []byte("Golf Quotation / Penawaran Golf")) {
		t.Fatal("golf quotation template")
	}
	sx.Must(422, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "line": "wedding", "title": "Unknown " + sfx,
		"lines": []map[string]any{{"itemType": "banquet_menu", "itemRef": "NOPE" + sfx, "description": "x", "quantity": "1"}}}, "Idempotency-Key", newKey())

	// Discount limits per role (§16 #5): Sales Executive 5%, Banquet Manager 20%.
	manual := []map[string]any{{"itemType": "service", "description": "Gala dinner", "quantity": "1", "unitPrice": "10000000"}}
	for _, c := range []struct {
		who  *Client
		pct  string
		want string
	}{{sx, "5", "not_required"}, {sx, "6", "required"}, {bm, "15", "not_required"}, {bm, "25", "required"}} {
		r := c.who.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "line": "event", "title": "Limit " + c.pct + " " + sfx,
			"discountPercent": c.pct, "lines": manual}, "Idempotency-Key", newKey()).JSON()
		if r["approvalStatus"] != c.want {
			t.Fatalf("discount %s%%: want %s, got %v", c.pct, c.want, r["approvalStatus"])
		}
	}

	// Sales Policies without automatic promotions: the catalogue price only.
	fixPolicy(t, sa, "crm.sales", func(v map[string]any) { v["quotationPromotions"] = false })
	np := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "line": "golf", "title": "No promo " + sfx,
		"lines": []map[string]any{lines[2]}}, "Idempotency-Key", newKey()).JSON()
	if !dec(np["total"]).Equal(decimal.NewFromInt(2_250_000)) || !dec(np["discount"]).IsZero() {
		t.Fatalf("promotions switched off: %v", np)
	}
}

func fixJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// fixVouchers counts the vouchers of a customer issued for a source type.
func fixVouchers(t *testing.T, customer, sourceType string) []string {
	t.Helper()
	return engIDs(t, `SELECT v.code || '|' || t.code || '|' || trim_scale(v.remaining_quantity)::text FROM commercial.vouchers v
		JOIN commercial.voucher_types t ON t.id = v.voucher_type_id WHERE v.customer_id = $1 AND v.source_type = $2 ORDER BY v.code`,
		mustUUID(customer), sourceType)
}

// FR-CMP-05 / FR-LOY-05 / FR-TKT-05: real Commercial vouchers — one per
// campaign recipient (idempotent when crm.campaign_sent repeats), the
// voucher of a loyalty voucher reward (handed over at once) and the value
// voucher of an approved voucher compensation; a refund compensation is
// paid back on the customer's latest payment, once.
func TestP3FixMoneyVouchers(t *testing.T) {
	sa := superAdmin(t, inst)
	mk := roleUser(t, inst, "marketing_staff")
	fd := roleUser(t, inst, "front_desk")
	gm := roleUser(t, inst, "golf_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	vt := "FV" + sfx
	sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": vt, "name": "Coffee voucher " + sfx, "kind": "value",
		"category": "fnb", "unit": "rupiah", "faceValue": "100000", "validityDays": 30})

	// Campaign voucher per recipient (FR-CMP-05).
	c1 := customer(t, sa, "FVA"+sfx, "Voucher Ana "+sfx, map[string]any{"email": "ana" + sfx + "@fv.test"})
	c2 := customer(t, sa, "FVB"+sfx, "Voucher Bob "+sfx, map[string]any{"email": "bob" + sfx + "@fv.test"})
	for _, c := range []string{c1, c2} {
		sysExec(t, inst, `INSERT INTO crm.communication_preferences (customer_id, channel, property_id, opted_in, source) VALUES ($1, 'email', $2, true, 'import')`,
			mustUUID(c), inst.Main)
	}
	seg := idOf(mk.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "FVS" + sfx, "name": "Voucher test " + sfx, "segmentType": "static"}))
	mk.Must(200, "POST", "/api/v1/crm/segments/"+seg+"/members:add", map[string]any{"customerIds": []string{c1, c2}})
	cid := idOf(mk.Must(201, "POST", "/api/v1/crm/campaigns", map[string]any{"code": "FVC" + sfx, "name": "Coffee on us " + sfx, "segmentId": seg,
		"channel": "email", "templateEvent": "crm.campaign_message", "subject": "Coffee", "body": "Halo {{.name}}", "voucherTypeRef": vt}))
	mk.Must(200, "POST", "/api/v1/crm/campaigns/"+cid+":schedule", map[string]any{})
	engCampaignDone(t, sa, cid)
	engDispatch(t, "campaign vouchers", func() bool {
		return len(fixVouchers(t, c1, "crm.campaign")) == 1 && len(fixVouchers(t, c2, "crm.campaign")) == 1
	})
	v1 := fixVouchers(t, c1, "crm.campaign")[0]
	if !strings.Contains(v1, "|"+vt+"|100000") {
		t.Fatalf("campaign voucher: %s", v1)
	}
	if l := sa.Must(200, "GET", "/api/v1/commercial/vouchers?filter[customerId]="+c1, nil).Items(); len(l) != 1 || l[0]["status"] != "active" {
		t.Fatalf("the recipient's vouchers: %v", l)
	}
	engPublish(t, "crm.campaign_sent", "crm.campaign", map[string]any{"campaignId": cid, "code": "FVC" + sfx, "name": "Coffee on us", "voucherTypeRef": vt,
		"recipients": []map[string]any{{"recipientId": cid, "customerId": c1}}})
	engDispatch(t, "repeated campaign event", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'crm.campaign_sent' AND aggregate_id IS NOT NULL AND dispatched_at IS NULL`,
			nil, &n)
		return n == 0
	})
	if n := len(fixVouchers(t, c1, "crm.campaign")); n != 1 {
		t.Fatalf("one voucher per recipient: %d", n)
	}

	// Loyalty voucher reward (FR-LOY-05): a real voucher, handed over at once.
	aid := idOf(fd.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": c1}))
	sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":adjust", map[string]any{"points": 500, "reason": "Welcome " + sfx})
	rw := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/rewards", map[string]any{"code": "FVR" + sfx, "name": "Coffee reward " + sfx,
		"rewardType": "voucher", "pointsCost": 200, "voucherTypeRef": vt}))
	red := fd.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":redeem-reward", map[string]any{"rewardId": rw}).JSON()
	rv := fixVouchers(t, c1, "crm.reward_redemption")
	if red["status"] != "completed" || len(rv) != 1 || !strings.HasPrefix(rv[0], str(red["fulfilmentCode"])+"|") || engBalance(t, sa, aid) != 300 {
		t.Fatalf("voucher reward: %v / %v", red, rv)
	}

	// Complaint compensations (FR-TKT-05): voucher and refund, once each.
	cat := idOf(sa.Must(201, "POST", "/api/v1/crm/ticket-categories", map[string]any{"code": "FVT" + sfx, "name": "Service " + sfx, "businessLine": "pos",
		"defaultPriority": "medium"}))
	tk := idOf(fd.Must(201, "POST", "/api/v1/crm/tickets", map[string]any{"customerId": c2, "categoryId": cat, "channel": "phone",
		"subject": "Cold soup", "description": "Soup served cold"}))
	folio := engFolio(t, sa, c2, "Dinner "+sfx, "300000")
	pay := sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": folio, "amount": "300000", "methodType": "cash", "channel": "venue"}).JSON()
	vc := gm.Must(201, "POST", "/api/v1/crm/tickets/"+tk+":compensate", map[string]any{"type": "voucher", "amount": "150000", "description": "Dinner voucher"}).JSON()
	rc := gm.Must(201, "POST", "/api/v1/crm/tickets/"+tk+":compensate", map[string]any{"type": "refund", "amount": "100000", "description": "Soup refunded"}).JSON()
	if vc["status"] != "approved" || rc["status"] != "approved" {
		t.Fatalf("compensations approved (no workflow): %v %v", vc, rc)
	}
	reason := "Complaint compensation " + str(rc["number"]) + " · ticket "
	engDispatch(t, "compensation voucher and refund", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM billing.refunds WHERE payment_id = $1 AND reason LIKE $2 || '%' AND status = 'completed'`,
			[]any{mustUUID(str(pay["id"])), reason}, &n)
		return n == 1 && len(fixVouchers(t, c2, "crm.ticket_compensation")) == 1
	})
	if v := fixVouchers(t, c2, "crm.ticket_compensation")[0]; !strings.Contains(v, "|COMPENSATION|150000") {
		t.Fatalf("compensation voucher of the approved amount: %s", v)
	}
	if p := sa.Must(200, "GET", "/api/v1/billing/payments/"+str(pay["id"]), nil).JSON(); !dec(p["refundedAmount"]).Equal(decimal.NewFromInt(100000)) {
		t.Fatalf("refund on the payment: %v", p)
	}
	// Redelivered approval events change nothing.
	for _, c := range []map[string]any{vc, rc} {
		engPublish(t, "crm.ticket_compensation_approved", "crm.ticket_compensation", map[string]any{"compensationId": c["id"], "number": c["number"],
			"ticketId": tk, "ticketNumber": "T", "customerId": c2, "type": c["type"], "amount": c["amount"], "description": "again"})
	}
	engDispatch(t, "redelivered compensations", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'crm.ticket_compensation_approved' AND dispatched_at IS NULL`, nil, &n)
		return n == 0
	})
	var refunds int
	sysQueryRow(t, inst, `SELECT count(*) FROM billing.refunds WHERE payment_id = $1`, []any{mustUUID(str(pay["id"]))}, &refunds)
	if refunds != 1 || len(fixVouchers(t, c2, "crm.ticket_compensation")) != 1 {
		t.Fatalf("compensations executed once: %d refunds", refunds)
	}
}

// FR-MIG-P3-02 / FR-MIG-P3-05 / §16 #17: the open corporate invoices of the
// legacy system from CSV or Excel — preview validates and reconciles
// without saving, commit imports (overdue invoices age at once), a re-run
// of the same file is idempotent, a changed amount is refused, and the
// migrated corporate AR equals the file and the club's control total.
func TestP3FixMoneyARImport(t *testing.T) {
	sa := superAdmin(t, inst)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	today := time.Now().In(clubLoc(inst))
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "FAR" + sfx, "name": "PT Legacy Debtor " + sfx}))
	customer(t, sa, "FARC"+sfx, "Legacy Person "+sfx, nil)
	past, future := today.AddDate(0, 0, -45), today.AddDate(0, 0, 20)
	header := "legacyNumber,corporateCode,customerCode,issueDate,dueDate,outstanding,originalTotal,description\n"
	rows := fmt.Sprintf("A%[1]s-1,FAR%[1]s,,%[2]s,%[3]s,12500000,15000000,Golf day March\n"+
		"A%[1]s-2,far%[1]s,,%[4]s,%[5]s,7500000,,Meeting package\n"+
		"A%[1]s-3,,FARC%[1]s,%[4]s,%[5]s,Rp 2000000,,Personal account\n",
		sfx, past.AddDate(0, 0, -30).Format("2006-01-02"), past.Format("2006-01-02"), today.Format("02/01/2006"), future.Format("2006-01-02"))
	bad := fmt.Sprintf("A%[1]s-4,NOPE%[1]s,,,,1000,,Unknown company\nA%[1]s-5,FAR%[1]s,,,,-5,,Negative\nA%[1]s-1,FAR%[1]s,,,,1,,Duplicate\n", sfx)

	pv := fin.Must(200, "POST", "/api/v1/billing/invoices:import", map[string]any{"mode": "preview", "csv": header + rows + bad,
		"expectedTotal": "22000000"}).JSON()
	if pv["imported"].(float64) != 3 || pv["failed"].(float64) != 3 || pv["fileTotal"] != "22000000" || pv["reconciled"] == true ||
		len(pv["errors"].([]any)) != 3 {
		t.Fatalf("preview: %v", pv)
	}
	if l := fin.Must(200, "GET", "/api/v1/billing/invoices?filter[corporateAccountId]="+corp, nil).Items(); len(l) != 0 {
		t.Fatalf("preview saves nothing: %v", l)
	}
	if r := fin.Do("POST", "/api/v1/billing/invoices:import", map[string]any{"mode": "commit", "csv": "foo,bar\n1,2\n"}); r.Status != 200 ||
		!strings.Contains(string(r.Body), "missing_column") {
		t.Fatalf("missing columns: %s", r)
	}
	roleUser(t, inst, "cashier").Must(403, "POST", "/api/v1/billing/invoices:import", map[string]any{"mode": "preview", "csv": header + rows})

	cm := fin.Must(200, "POST", "/api/v1/billing/invoices:import", map[string]any{"mode": "commit", "csv": header + rows, "expectedTotal": "22000000"}).JSON()
	if cm["imported"].(float64) != 3 || cm["failed"].(float64) != 0 || cm["reconciled"] != true || cm["migratedOpen"] != "22000000" || cm["difference"] != "0" {
		t.Fatalf("commit with the AR reconciliation: %v", cm)
	}
	var company map[string]any
	for _, c := range cm["companies"].([]any) {
		if m := c.(map[string]any); m["code"] == "FAR"+sfx {
			company = m
		}
	}
	if company == nil || company["invoices"].(float64) != 2 || company["outstanding"] != "20000000" {
		t.Fatalf("AR per company: %v", cm["companies"])
	}
	invs := fin.Must(200, "GET", "/api/v1/billing/invoices?filter[corporateAccountId]="+corp, nil).Items()
	status := map[string]string{}
	for _, i := range invs {
		status[str(i["number"])] = str(i["status"])
	}
	if len(invs) != 2 || status["RH-A"+sfx+"-1"] != "overdue" || status["RH-A"+sfx+"-2"] != "issued" {
		t.Fatalf("migrated invoices: %v", invs)
	}
	// Delta re-run: idempotent; a changed amount is refused.
	again := fin.Must(200, "POST", "/api/v1/billing/invoices:import", map[string]any{"mode": "commit", "csv": header + rows}).JSON()
	if again["imported"].(float64) != 0 || again["existing"].(float64) != 3 || again["reconciled"] != true {
		t.Fatalf("re-run: %v", again)
	}
	changed := fin.Must(200, "POST", "/api/v1/billing/invoices:import", map[string]any{"mode": "commit",
		"csv": header + "A" + sfx + "-2,FAR" + sfx + ",,,,7000000,,Meeting package\n"}).JSON()
	if changed["failed"].(float64) != 1 || !strings.Contains(fixJSON(changed["errors"]), "changed") {
		t.Fatalf("changed amount: %v", changed)
	}
	// The Excel workbook of the next batch.
	f := excelize.NewFile()
	for i, v := range []string{"legacyNumber", "corporateCode", "outstanding", "dueDate"} {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue("Sheet1", cell, v)
	}
	for i, v := range []string{"A" + sfx + "-6", "FAR" + sfx, "3000000", future.Format("2006-01-02")} {
		cell, _ := excelize.CoordinatesToCellName(i+1, 2)
		_ = f.SetCellValue("Sheet1", cell, v)
	}
	var xb bytes.Buffer
	if err := f.Write(&xb); err != nil {
		t.Fatal(err)
	}
	xl := fin.Must(200, "POST", "/api/v1/billing/invoices:import", map[string]any{"mode": "commit", "xlsx": base64.StdEncoding.EncodeToString(xb.Bytes()),
		"expectedTotal": "3000000"}).JSON()
	if xl["imported"].(float64) != 1 || xl["fileTotal"] != "3000000" || xl["migratedOpen"] != "25000000" || xl["reconciled"] == true ||
		xl["difference"] != "-22000000" {
		t.Fatalf("Excel batch (the migrated AR now includes the earlier batch): %v", xl)
	}
	fin.Must(422, "POST", "/api/v1/billing/invoices:import", map[string]any{"mode": "commit", "xlsx": "bm90IGV4Y2Vs"})
	// The migrated invoices are the opening AR of the company account.
	acct := sa.Must(200, "GET", "/api/v1/billing/invoices?filter[corporateAccountId]="+corp, nil).Items()
	if len(acct) != 3 {
		t.Fatalf("company invoices after the Excel batch: %v", acct)
	}
}

// FR-LOY-02 / FR-LOY-10: a spend rule of a CRM segment multiplies the
// points of its members (the same spend earns twice the points of a
// customer outside the segment); the daily redemption limit of the Loyalty
// Policies refuses a second redemption the same day.
func TestP3FixMoneyLoyalty(t *testing.T) {
	sa := superAdmin(t, inst)
	fd := roleUser(t, inst, "front_desk")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	vip := customer(t, sa, "FLV"+sfx, "Segment VIP "+sfx, map[string]any{"email": "vip" + sfx + "@fl.test"})
	reg := customer(t, sa, "FLR"+sfx, "Regular "+sfx, map[string]any{"email": "reg" + sfx + "@fl.test"})
	seg := idOf(sa.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "FLS" + sfx, "name": "Loyalty VIP " + sfx, "segmentType": "static"}))
	sa.Must(200, "POST", "/api/v1/crm/segments/"+seg+"/members:add", map[string]any{"customerIds": []string{vip}})
	rule := sa.Must(201, "POST", "/api/v1/crm/loyalty/earning-rules", map[string]any{"code": "FLM" + sfx, "name": "VIP × 2 " + sfx, "ruleType": "spend",
		"multiplier": "2", "customerSegmentId": seg}).JSON()
	if rule["customerSegmentId"] != seg {
		t.Fatalf("segment earning rule: %v", rule)
	}
	sa.Must(422, "POST", "/api/v1/crm/loyalty/earning-rules", map[string]any{"code": "FLX" + sfx, "name": "Bad segment", "ruleType": "spend",
		"customerSegmentId": vip})
	accts := map[string]string{}
	for _, c := range []string{vip, reg} {
		accts[c] = idOf(fd.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": c}))
		f := engFolio(t, sa, c, "Spend "+sfx, "1000000")
		sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": f, "amount": "1000000", "methodType": "cash", "channel": "venue"})
	}
	engDispatch(t, "points earned", func() bool { return engBalance(t, sa, accts[vip]) > 0 && engBalance(t, sa, accts[reg]) > 0 })
	if v, r := engBalance(t, sa, accts[vip]), engBalance(t, sa, accts[reg]); r < 100 || v != 2*r {
		t.Fatalf("segment multiplier: VIP %d points, regular %d points", v, r)
	}

	// Daily redemption limit (FR-LOY-10).
	fixPolicy(t, sa, "crm.loyalty", func(v map[string]any) { v["maxRedeemPointsPerDay"] = 150 })
	sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+accts[reg]+":adjust", map[string]any{"points": 500, "reason": "Goodwill " + sfx})
	f2 := engFolio(t, sa, reg, "Lunch "+sfx, "500000")
	fd.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+accts[reg]+":redeem", map[string]any{"folioId": f2, "points": 100}, "Idempotency-Key", newKey())
	r := fd.Do("POST", "/api/v1/crm/loyalty/accounts/"+accts[reg]+":redeem", map[string]any{"folioId": f2, "points": 60}, "Idempotency-Key", newKey())
	if r.Status != 409 || !strings.Contains(string(r.Body), "daily_limit") {
		t.Fatalf("daily redemption limit: %s", r)
	}
	fd.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+accts[reg]+":redeem", map[string]any{"folioId": f2, "points": 50}, "Idempotency-Key", newKey())
}

// FR-PRM-01: Buy N Get X (3 for 2), Bundle (cake + coffee) and Happy Hour
// (30% on juice 16:00–18:00 on weekdays) priced by the promotion engine.
func TestP3FixMoneyPromotionTypes(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	product := func(code, price string) string {
		return idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": code + sfx, "name": code + " " + sfx, "category": "Fix",
			"productType": "beverage", "price": price}))
	}
	coffee, cake, juice := product("FPCOF", "40000"), product("FPCAKE", "60000"), product("FPJUI", "50000")
	promo := func(body map[string]any) string {
		body["name"] = str(body["code"]) + " " + sfx
		return idOf(sa.Must(201, "POST", "/api/v1/commercial/promotions", body))
	}
	b3 := promo(map[string]any{"code": "FP-B3" + sfx, "promoType": "buy_n_get_x", "buyQuantity": 2, "getQuantity": 1, "productIds": []string{coffee}})
	bundle := promo(map[string]any{"code": "FP-BU" + sfx, "promoType": "bundle", "bundlePrice": "80000",
		"bundleItems": []map[string]any{{"productId": cake, "quantity": 1}, {"productId": coffee, "quantity": 1}}})
	hh := promo(map[string]any{"code": "FP-HH" + sfx, "promoType": "happy_hour", "discountPercent": "30", "productIds": []string{juice},
		"timeWindows": []map[string]any{{"days": []int{1, 2, 3, 4, 5}, "start": "16:00", "end": "18:00"}}})
	simulate := func(id string, at time.Time, lines ...map[string]any) []decimal.Decimal {
		scen := map[string]any{"label": "s", "at": at.UTC().Format(time.RFC3339), "channel": "pos", "lines": lines}
		res := sa.Must(200, "POST", "/api/v1/commercial/promotions/"+id+":simulate", map[string]any{"scenarios": []map[string]any{scen}}).JSON()
		s := res["scenarios"].([]any)[0].(map[string]any)
		return []decimal.Decimal{dec(s["total"]), dec(s["discount"])}
	}
	line := func(p, qty, price string) map[string]any {
		return map[string]any{"productId": p, "quantity": qty, "unitPrice": price}
	}
	noon := pcLocal(time.Wednesday, 12, 0)
	for _, c := range []struct {
		name  string
		got   []decimal.Decimal
		total int64
	}{
		{"buy 2 get 1: three coffees", simulate(b3, noon, line(coffee, "3", "40000")), 80000},
		{"buy 2 get 1: two coffees", simulate(b3, noon, line(coffee, "2", "40000")), 80000},
		{"buy 2 get 1: six coffees", simulate(b3, noon, line(coffee, "6", "40000")), 160000},
		{"bundle cake + coffee", simulate(bundle, noon, line(cake, "1", "60000"), line(coffee, "1", "40000")), 80000},
		{"bundle incomplete", simulate(bundle, noon, line(cake, "1", "60000")), 60000},
		{"happy hour 17:00", simulate(hh, pcLocal(time.Wednesday, 17, 0), line(juice, "2", "50000")), 70000},
		{"after happy hour", simulate(hh, pcLocal(time.Wednesday, 19, 0), line(juice, "2", "50000")), 100000},
		{"weekend", simulate(hh, pcLocal(time.Saturday, 17, 0), line(juice, "2", "50000")), 100000},
	} {
		if !c.got[0].Equal(decimal.NewFromInt(c.total)) {
			t.Fatalf("%s: total %s (discount %s), want %d", c.name, c.got[0], c.got[1], c.total)
		}
	}
}

// fixCommission returns the commission lines of a quotation by kind.
func fixCommission(c *Client, qid, kind string) []map[string]any {
	var out []map[string]any
	for _, l := range c.Must(200, "GET", "/api/v1/crm/commission-lines?filter[quotationId]="+qid, nil).Items() {
		if l["kind"] == kind {
			out = append(out, l)
		}
	}
	return out
}

// fixGolfDeal creates and accepts a golf quotation of the user (one 100%
// term) and pays its schedule; it returns the quotation id.
func fixGolfDeal(t *testing.T, c, fin *Client, cust, title, amount string) string {
	t.Helper()
	q := c.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "line": "golf", "title": title, "pricingMode": "nett",
		"paymentTerms": []map[string]any{{"label": "Full Payment", "percent": "100", "dueDays": 3}},
		"lines":        []map[string]any{{"itemType": "service", "description": "Golf day", "quantity": "1", "unitPrice": amount}}},
		"Idempotency-Key", newKey()).JSON()
	c.Must(200, "POST", "/api/v1/crm/quotations/"+str(q["id"])+":accept", map[string]any{"acceptedByName": "PIC"})
	var sched map[string]any
	slsDispatch(t, "schedule of "+title, func() bool {
		for _, s := range fin.Must(200, "GET", "/api/v1/billing/payment-schedules?filter[sourceType]=quotation&limit=200", nil).Items() {
			if s["sourceRef"] == q["number"] {
				sched = s
			}
		}
		return sched != nil
	})
	sd := fin.Must(200, "GET", "/api/v1/billing/payment-schedules/"+str(sched["id"]), nil).JSON()
	for _, l := range sd["lines"].([]any) {
		fin.Must(201, "POST", "/api/v1/billing/payment-schedules/"+str(sched["id"])+"/lines/"+str(l.(map[string]any)["id"])+":pay",
			map[string]any{"methodType": "bank_transfer", "reference": "TRF-" + title})
	}
	return str(q["id"])
}

// FR-COM-03 / FR-COM-05 / EP-04 AC / §16 #4 / EP-02 AC: a tiered scheme
// pays the tier of the target achievement, a flat scheme the flat amount
// per deal; the wedding of Rp88 jt paid in full earns Rp880.000 (1% of the
// net, Sales Policies) and its refund within 90 days claws back
// −Rp880.000; the weighted pipeline value is Σ value × stage probability.
func TestP3FixMoneyCommission(t *testing.T) {
	sa := superAdmin(t, inst)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	ca := roleUser(t, inst, "crm_admin")
	bm := roleUser(t, inst, "banquet_manager")
	bs := roleUser(t, inst, "banquet_sales")
	caID, bmID := slsUserID(t, "role.crm_admin@matrix.test"), slsUserID(t, "role.banquet_manager@matrix.test")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	loc := clubLoc(inst)
	today := time.Now().In(loc)
	from := today.AddDate(0, 0, -1).Format("2006-01-02")
	cust := customer(t, sa, "FCM"+sfx, "Commission Client "+sfx, map[string]any{"email": "cm" + sfx + "@fix.test"})

	// Tiered: 2% below 50% of the target, 4% from 50% (6 jt of a 10 jt target → 4%).
	sa.Must(201, "POST", "/api/v1/crm/commission-schemes", map[string]any{"code": "FCT" + sfx, "name": "Tiered " + sfx, "schemeType": "tiered",
		"basis": "net", "tiers": []map[string]any{{"minAchievementPercent": "0", "percent": "2"}, {"minAchievementPercent": "50", "percent": "4"}},
		"userId": caID, "effectiveFrom": from})
	sa.Must(201, "POST", "/api/v1/crm/sales-targets", map[string]any{"userId": caID, "line": "golf", "periodType": "month",
		"periodStart": time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02"),
		"periodEnd":   time.Date(today.Year(), today.Month()+1, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), "targetRevenue": "10000000"})
	tq := fixGolfDeal(t, ca, fin, cust, "Tiered "+sfx, "6000000")
	// Flat: Rp750.000 per golf deal whatever its value.
	sa.Must(201, "POST", "/api/v1/crm/commission-schemes", map[string]any{"code": "FCF" + sfx, "name": "Flat " + sfx, "schemeType": "flat",
		"rates": []map[string]any{{"line": "golf", "flatAmount": "750000"}}, "userId": bmID, "effectiveFrom": from})
	fq := fixGolfDeal(t, bm, fin, cust, "Flat "+sfx, "23000000")
	slsDispatch(t, "tiered and flat commission", func() bool {
		return len(fixCommission(fin, tq, "earned")) == 1 && len(fixCommission(fin, fq, "earned")) == 1
	})
	if e := fixCommission(fin, tq, "earned")[0]; !dec(e["amount"]).Equal(decimal.NewFromInt(240000)) || !dec(e["ratePercent"]).Equal(decimal.NewFromInt(4)) ||
		str(e["userId"]) != caID {
		t.Fatalf("tiered commission (4%% of 6.000.000): %v", e)
	}
	if e := fixCommission(fin, fq, "earned")[0]; !dec(e["amount"]).Equal(decimal.NewFromInt(750000)) {
		t.Fatalf("flat commission: %v", e)
	}

	// Wedding Rp88 jt (banquet event from the quotation), paid in full → Rp880.000.
	hall := bqVenue(t, sa, map[string]any{"code": "FCH" + sfx, "name": "Commission Hall " + sfx, "venueType": "ballroom", "maxCapacity": 400})
	wq := bs.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "line": "wedding", "eventType": "wedding",
		"title": "Wedding " + sfx, "eventDate": today.AddDate(0, 0, 100).Format("2006-01-02"), "pax": 300, "venueResourceId": hall["resourceId"],
		"pricingMode": "nett", "paymentTerms": []map[string]any{{"label": "DP 30%", "percent": "30", "dueDays": 3}, {"label": "Final Payment", "percent": "70", "dueDays": 30}},
		"lines": []map[string]any{{"itemType": "service", "description": "Wedding package 300 pax", "quantity": "1", "unitPrice": "88000000"}}},
		"Idempotency-Key", newKey()).JSON()
	wid := str(wq["id"])
	bs.Must(200, "POST", "/api/v1/crm/quotations/"+wid+":accept", map[string]any{"acceptedByName": "Bride"})
	var eid string
	slsDispatch(t, "wedding event", func() bool {
		eid, _ = bqQuotationEvent(t, wq["number"])
		if eid == "" {
			return false
		}
		return sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()["scheduleId"] != nil
	})
	ev := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()
	sc := sa.Must(200, "GET", "/api/v1/billing/payment-schedules/"+str(ev["scheduleId"]), nil).JSON()
	var payments []map[string]any
	for i, l := range sc["lines"].([]any) {
		payments = append(payments, fin.Must(201, "POST", "/api/v1/billing/payment-schedules/"+str(sc["id"])+"/lines/"+str(l.(map[string]any)["id"])+":pay",
			map[string]any{"methodType": "bank_transfer", "reference": fmt.Sprintf("WED-%s-%d", sfx, i)}).JSON())
	}
	slsDispatch(t, "wedding commission", func() bool { return len(fixCommission(fin, wid, "earned")) == 1 })
	if e := fixCommission(fin, wid, "earned")[0]; !dec(e["amount"]).Equal(decimal.NewFromInt(880000)) || !dec(e["basisAmount"]).Equal(decimal.NewFromInt(88_000_000)) {
		t.Fatalf("AC: wedding Rp88.000.000 → commission Rp880.000: %v", e)
	}
	// Refunded within 90 days → clawback −Rp880.000 in the current period.
	for _, p := range payments {
		rf := sa.Must(201, "POST", "/api/v1/billing/refunds", map[string]any{"paymentId": p["id"], "amount": p["amount"], "reason": "Wedding cancelled " + sfx}).JSON()
		if rf["status"] == "pending" { // above the Refund Policy limit
			fin.Must(200, "POST", "/api/v1/platform/approvals/"+str(rf["approvalRequestId"])+":approve", map[string]any{})
		}
	}
	slsDispatch(t, "clawback", func() bool {
		total := decimal.Zero
		for _, l := range fixCommission(fin, wid, "clawback") {
			total = total.Add(dec(l["amount"]))
		}
		return total.Equal(decimal.NewFromInt(-880000))
	})
	for _, l := range fixCommission(fin, wid, "clawback") {
		if l["period"] != today.Format("2006-01") {
			t.Fatalf("clawback period: %v", l)
		}
	}
	var audited int
	sysQueryRow(t, inst, `SELECT count(*) FROM audit.audit_log WHERE action = 'clawback' AND entity_type = 'crm.sales_commission' AND entity_label LIKE $1 || '%'`,
		[]any{str(wq["number"])}, &audited)
	if audited < 1 {
		t.Fatal("clawback audited")
	}

	// EP-02 AC: weighted value = Σ value × probability.
	_, stages := slsPipeline(t, sa, "WEDDING")
	o1 := ca.Must(201, "POST", "/api/v1/crm/opportunities", map[string]any{"title": "Forecast A " + sfx, "line": "wedding", "customerId": cust,
		"expectedValue": "100000000", "expectedCloseDate": today.AddDate(0, 1, 0).Format("2006-01-02")}).JSON()
	o2 := ca.Must(201, "POST", "/api/v1/crm/opportunities", map[string]any{"title": "Forecast B " + sfx, "line": "wedding", "customerId": cust,
		"expectedValue": "50000000", "expectedCloseDate": today.AddDate(0, 1, 0).Format("2006-01-02")}).JSON()
	ca.Must(200, "POST", "/api/v1/crm/opportunities/"+str(o2["id"])+":move-stage", map[string]any{"stageId": stages["QUOTATION"]})
	b := ca.Must(200, "GET", "/api/v1/crm/pipelines/"+str(o1["pipelineId"])+"/board?q=Forecast+%25"+sfx, nil).JSON()
	if b["openCount"].(float64) != 2 || !dec(b["openValue"]).Equal(decimal.NewFromInt(150_000_000)) ||
		!dec(b["weightedValue"]).Equal(decimal.NewFromInt(35_000_000)) {
		t.Fatalf("weighted pipeline value (100 jt × 10%% + 50 jt × 50%%): %v", b)
	}
}

// EP-17 AC2 / FR-INT-P3-04 / §16 #11: an invoice past its due date turns
// Overdue with billing.invoice_overdue and a reminder (once); a voided
// invoice publishes billing.invoice_voided; online payments resolve their
// gateway per method through the integration layer; the default banquet
// schedule has the second term of 40% at H-60.
func TestP3FixMoneyBilling(t *testing.T) {
	sa := superAdmin(t, inst)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	today := time.Now().In(clubLoc(inst))
	sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "FBI" + sfx, "name": "PT Reminder " + sfx, "email": "ar" + sfx + "@fix.test"})
	csv := "legacyNumber,corporateCode,dueDate,outstanding\n" +
		"B" + sfx + "-1,FBI" + sfx + "," + today.AddDate(0, 0, 10).Format("2006-01-02") + ",4000000\n" +
		"B" + sfx + "-2,FBI" + sfx + "," + today.AddDate(0, 0, 10).Format("2006-01-02") + ",1000000\n"
	fin.Must(200, "POST", "/api/v1/billing/invoices:import", map[string]any{"mode": "commit", "csv": csv})
	var inv1, inv2 string
	sysQueryRow(t, inst, `SELECT (SELECT id::text FROM billing.invoices WHERE number = $1), (SELECT id::text FROM billing.invoices WHERE number = $2)`,
		[]any{"RH-B" + sfx + "-1", "RH-B" + sfx + "-2"}, &inv1, &inv2)

	// Overdue → status, event and reminder (EP-17 AC2), once.
	sysExec(t, inst, `UPDATE billing.invoices SET due_date = $2::date WHERE id = $1`, mustUUID(inv1), today.AddDate(0, 0, -1).Format("2006-01-02"))
	remind := func() {
		ctx := dbtx.System(t.Context())
		if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
			_, err := inst.App.BillingHTTP.InvoiceReminders(ctx, tx, inst.Main)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	remind()
	remind()
	d := fin.Must(200, "GET", "/api/v1/billing/invoices/"+inv1, nil).JSON()
	if d["status"] != "overdue" || d["daysOverdue"].(float64) < 1 {
		t.Fatalf("overdue invoice: %v", d)
	}
	var overdueEvents, reminders int
	sysQueryRow(t, inst, `SELECT (SELECT count(*) FROM platform.outbox WHERE event_type = 'billing.invoice_overdue' AND payload->>'invoiceId' = $1),
		(SELECT count(*) FROM audit.audit_log WHERE action = 'reminder' AND entity_id = $1)`, []any{inv1}, &overdueEvents, &reminders)
	if overdueEvents != 1 || reminders != 1 {
		t.Fatalf("overdue once: %d events, %d reminders", overdueEvents, reminders)
	}
	var reminderSent int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notification_deliveries WHERE event_code = 'billing.invoice_reminder' AND recipient = $1`,
		[]any{"ar" + sfx + "@fix.test"}, &reminderSent)
	if reminderSent == 0 {
		t.Fatal("reminder e-mail to the company")
	}

	// Void → billing.invoice_voided.
	v := fin.Must(200, "POST", "/api/v1/billing/invoices/"+inv2+":void", map[string]any{"reason": "Duplicate legacy invoice"}).JSON()
	if v["status"] != "void" {
		t.Fatalf("voided: %v", v)
	}
	if pcEvents(t, "billing.invoice_voided", "invoiceId", inv2) != 1 {
		t.Fatal("billing.invoice_voided published")
	}

	// Online payments: the gateway of the method at the property (integration layer).
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	before := pa.Must(200, "GET", "/api/v1/platform/integrations/"+mp, nil).JSON()
	settings, _ := before["settings"].(map[string]any)
	restore := map[string]any{"enabled": before["enabled"], "settings": settings}
	t.Cleanup(func() { pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, restore) })
	only := map[string]any{"methods": []string{"qris"}, "autoPay": false}
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": only})
	cust := customer(t, sa, "FBC"+sfx, "Online Payer "+sfx, map[string]any{"email": "pay" + sfx + "@fix.test"})
	folio := engFolio(t, sa, cust, "Online "+sfx, "200000")
	q := sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": folio, "amount": "100000", "methodType": "qris", "channel": "online"}).JSON()
	var code string
	sysQueryRow(t, inst, `SELECT coalesce(integration_code, '') FROM billing.payments WHERE id = $1`, []any{mustUUID(str(q["id"]))}, &code)
	if q["status"] != "pending" || code != "mock-payment" {
		t.Fatalf("QRIS through the gateway serving QRIS: %v (%s)", q, code)
	}
	va := sa.Do("POST", "/api/v1/billing/payments", map[string]any{"folioId": folio, "amount": "100000", "methodType": "virtual_account", "channel": "online"})
	if va.Status == 201 {
		var vcode string
		sysQueryRow(t, inst, `SELECT coalesce(integration_code, '') FROM billing.payments WHERE id = $1`, []any{mustUUID(str(va.JSON()["id"]))}, &vcode)
		if vcode == "mock-payment" {
			t.Fatalf("virtual account routed to a gateway that serves QRIS only: %s", va)
		}
	} else if va.Status != 503 {
		t.Fatalf("no gateway for virtual accounts: %s", va)
	}

	// Banquet Policies default schedule: DP 30%, 40% at H-60, the balance at H-7 (§16 #11).
	typ := bqType(t, sa, "FBT"+sfx, "wedding")
	hall := bqVenue(t, sa, map[string]any{"code": "FBH" + sfx, "name": "Terms Hall " + sfx, "venueType": "ballroom", "maxCapacity": 300})
	bp := idOf(sa.Must(201, "POST", "/api/v1/banquet/packages", map[string]any{"code": "FBP" + sfx, "name": "Terms package " + sfx, "category": "wedding",
		"pricingMethod": "fixed", "price": "100000000", "pricingMode": "nett"}))
	day := bqDay(120, 11, 0)
	ev := sa.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": "Terms wedding " + sfx, "eventTypeId": typ, "customerId": cust,
		"start": rfc(day), "end": rfc(day.Add(5 * time.Hour)), "expectedPax": 200, "packageId": bp,
		"venues": []map[string]any{{"venueId": hall["id"], "functionName": "Reception"}}}).JSON()
	bl := sa.Must(201, "POST", "/api/v1/banquet/events/"+str(ev["id"])+"/payment-schedule", map[string]any{}).JSON()
	lines := bl["schedule"].(map[string]any)["lines"].([]any)
	if len(lines) != 3 {
		t.Fatalf("three payment terms: %v", lines)
	}
	second := lines[1].(map[string]any)
	bqEq(t, "DP 30%", lines[0].(map[string]any)["amount"], "30000000")
	bqEq(t, "second term 40%", second["amount"], "40000000")
	bqEq(t, "balance", lines[2].(map[string]any)["amount"], "30000000")
	if second["dueDate"] != day.AddDate(0, 0, -60).Format("2006-01-02") || lines[2].(map[string]any)["dueDate"] != day.AddDate(0, 0, -7).Format("2006-01-02") {
		t.Fatalf("due dates H-60 and H-7: %v", lines)
	}
}

// FR-CMP-03 / FR-CMP-04: the frequency cap of the Campaign Policies skips
// a customer who already received the maximum of campaigns in the window;
// WhatsApp delivered / read statuses of the BSP webhook count in the
// campaign statistics.
func TestP3FixMoneyCampaignTracking(t *testing.T) {
	sa := superAdmin(t, inst)
	mk := roleUser(t, inst, "marketing_staff")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	a := customer(t, sa, "FTA"+sfx, "Track Ana "+sfx, map[string]any{"phone": "+62853" + sfx})
	b := customer(t, sa, "FTB"+sfx, "Track Budi "+sfx, map[string]any{"phone": "+62854" + sfx})
	for _, c := range []string{a, b} {
		sysExec(t, inst, `INSERT INTO crm.communication_preferences (customer_id, channel, property_id, opted_in, source) VALUES ($1, 'whatsapp', $2, true, 'import')`,
			mustUUID(c), inst.Main)
	}
	segAB := idOf(mk.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "FTS" + sfx, "name": "Tracking " + sfx, "segmentType": "static"}))
	mk.Must(200, "POST", "/api/v1/crm/segments/"+segAB+"/members:add", map[string]any{"customerIds": []string{a, b}})
	segA := idOf(mk.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "FTX" + sfx, "name": "Tracking A " + sfx, "segmentType": "static"}))
	mk.Must(200, "POST", "/api/v1/crm/segments/"+segA+"/members:add", map[string]any{"customerIds": []string{a}})
	fixPolicy(t, sa, "crm.campaign", func(v map[string]any) { v["frequencyCapCount"], v["frequencyCapDays"] = 2, 30 })
	send := func(code, seg string) string {
		cid := idOf(mk.Must(201, "POST", "/api/v1/crm/campaigns", map[string]any{"code": code + sfx, "name": code + " " + sfx, "segmentId": seg,
			"channel": "whatsapp", "templateEvent": "crm.campaign_message", "subject": code, "body": "Halo {{.name}}"}))
		mk.Must(200, "POST", "/api/v1/crm/campaigns/"+cid+":schedule", map[string]any{})
		engCampaignDone(t, sa, cid)
		return cid
	}
	first := send("FT1", segAB)

	// BSP statuses: A's message delivered then read, B's delivered.
	pa := platformAdmin(t, inst)
	wa := integrationID(t, inst, "mock-whatsapp")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+wa, map[string]any{"enabled": true})
	secret := str(pa.Must(200, "POST", "/api/v1/platform/integrations/"+wa+":rotate-webhook-secret", nil).JSON()["webhookSecret"])
	msgID := func(customer string) string {
		ext := "wamid.fix." + customer
		sysExec(t, inst, `UPDATE platform.notification_deliveries d SET external_id = $3 FROM crm.campaign_recipients r
			WHERE r.token = d.payload->>'trackingToken' AND r.campaign_id = $1 AND r.customer_id = $2 AND d.channel = 'whatsapp'`,
			mustUUID(first), mustUUID(customer), ext)
		return ext
	}
	ma, mb := msgID(a), msgID(b)
	hook := anon(t, inst)
	hook.Property = uuid.Nil
	post := func(statuses ...map[string]any) {
		body, _ := json.Marshal(map[string]any{"id": "st-" + uuid.NewString(), "type": "message.status", "data": map[string]any{"statuses": statuses}})
		hook.Must(200, "POST", "/api/v1/webhooks/mock-whatsapp", body, integration.SignatureHeader, integration.Sign(secret, body, time.Now()))
	}
	post(map[string]any{"messageId": ma, "status": "delivered"}, map[string]any{"messageId": mb, "status": "delivered"})
	post(map[string]any{"messageId": ma, "status": "read"})
	engDispatch(t, "delivery statuses", func() bool {
		st := mk.Must(200, "GET", "/api/v1/crm/campaigns/"+first+"/stats", nil).JSON()
		return st["delivered"].(float64) == 2 && st["read"].(float64) == 1
	})

	// Frequency cap 2 per 30 days: A gets the second campaign, not the third.
	send("FT2", segA)
	third := send("FT3", segAB)
	st := engRecipientStatus(t, third)
	if st[a] != "skipped_frequency_cap" || st[b] != "sent" {
		t.Fatalf("frequency cap: A %s, B %s", st[a], st[b])
	}
	if s := mk.Must(200, "GET", "/api/v1/crm/campaigns/"+third+"/stats", nil).JSON(); s["skipped"].(map[string]any)["frequency_cap"].(float64) != 1 {
		t.Fatalf("capped recipients in the stats: %v", s)
	}
}
