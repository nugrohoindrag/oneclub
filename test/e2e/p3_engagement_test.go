package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
)

// engDispatch runs the outbox until cond holds.
func engDispatch(t *testing.T, what string, cond func() bool) {
	t.Helper()
	slsDispatch(t, what, cond)
}

// engPublish publishes a domain event as another module would (contract
// payloads of docs/p3-p4-contracts.md).
func engPublish(t *testing.T, eventType, aggregate string, payload map[string]any) {
	t.Helper()
	ctx := dbtx.System(context.Background())
	aid := uuid.New()
	main := inst.Main
	if err := inst.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := inst.App.Bus.Publish(ctx, tx, eventType, aggregate, &aid, &main, payload)
		return err
	}); err != nil {
		t.Fatalf("publish %s: %v", eventType, err)
	}
}

// engBalance is the points balance of a loyalty account.
func engBalance(t *testing.T, c *Client, account string) int64 {
	t.Helper()
	return int64(c.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+account, nil).JSON()["balance"].(float64))
}

// engFolio opens a walk-in folio of a customer with one charge.
func engFolio(t *testing.T, c *Client, customer, desc, amount string) string {
	t.Helper()
	f := idOf(c.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": desc, "customerId": customer}))
	c.Must(201, "POST", "/api/v1/billing/folios/"+f+"/lines", map[string]any{"chargeType": "other", "description": desc, "unitPrice": amount})
	return f
}

// engReport runs a report and returns its rows.
func engReport(t *testing.T, c *Client, code, query string) []map[string]any {
	t.Helper()
	res := c.Must(200, "GET", "/api/v1/reporting/reports/"+code+"?"+query, nil).JSON()
	raw, ok := res["rows"].([]any)
	if !ok {
		t.Fatalf("report %s: %v", code, res)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func engToday() string { return time.Now().In(clubLoc(inst)).Format("2006-01-02") }

// EP-09 Loyalty Foundation acceptance: a settled payment of Rp1.250.000 at
// 1 point per Rp10.000 earns 125 points once although the settlement event
// arrives three times; a full refund reverses the 125 points; balance ×
// redemption value = Loyalty Liability Report = liability of the day.
// Also: opt-in, tiers (manual, locked, evaluated), points as a folio tender
// (front desk / Member App), rewards with fulfilment and cancel, Adjust
// Points through approval, expiry with notice, member referral reward,
// account status, opening balance import and the accounting export rows.
func TestP3EngagementLoyalty(t *testing.T) {
	sa := superAdmin(t, inst)
	fd := roleUser(t, inst, "front_desk")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	cust := customer(t, sa, "LOY"+sfx, "Loyal Lukas "+sfx, map[string]any{"email": "lukas" + sfx + "@loy.test", "phone": "+62811" + sfx})

	// Tiers and an earning rule for walk-in charges (line other) at 1 point per Rp10.000.
	base := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/tiers", map[string]any{"code": "LB" + sfx, "name": "Base " + sfx, "rank": 800, "multiplier": "1"}))
	gold := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/tiers", map[string]any{"code": "LG" + sfx, "name": "Gold " + sfx, "rank": 900,
		"minPoints": 400, "multiplier": "1.5", "benefits": "Points × 1.5"}))
	sa.Must(201, "POST", "/api/v1/crm/loyalty/earning-rules", map[string]any{"code": "LR" + sfx, "name": "Walk-in spend", "ruleType": "spend",
		"businessLine": "other", "amountPerPoint": "10000"})
	if r := sa.Do("POST", "/api/v1/crm/loyalty/earning-rules", map[string]any{"code": "LA" + sfx, "name": "No points", "ruleType": "activity"}); r.Status != 422 {
		t.Fatalf("activity rule needs an activity and points: %s", r)
	}

	// Opt-in (FR-LOY-01): no points before the customer joins.
	acct := fd.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": cust}).JSON()
	aid := str(acct["id"])
	if acct["status"] != "active" || acct["balance"].(float64) != 0 {
		t.Fatalf("enrol: %v", acct)
	}
	sa.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":set-tier", map[string]any{"tierId": base, "lock": true, "reason": "Pilot member"})

	// AC: Rp1.250.000 settled → 125 points, once.
	folio := engFolio(t, sa, cust, "Dinner "+sfx, "1250000")
	pay := sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": folio, "amount": "1250000", "methodType": "cash", "channel": "venue"}).JSON()
	engDispatch(t, "points earned", func() bool { return engBalance(t, sa, aid) > 0 })
	if b := engBalance(t, sa, aid); b != 125 {
		t.Fatalf("1.250.000 at 1 point / 10.000 = 125 points, got %d", b)
	}
	settled := map[string]any{"paymentId": pay["id"], "number": pay["number"], "folioId": folio, "amount": "1250000", "currency": "IDR",
		"methodType": "cash", "purpose": "settlement"}
	engPublish(t, "billing.payment_settled", "billing.payment", settled)
	engPublish(t, "billing.payment_settled", "billing.payment", settled)
	engDispatch(t, "duplicate settlements processed", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'billing.payment_settled' AND payload->>'paymentId' = $1
			AND dispatched_at IS NULL`, []any{str(pay["id"])}, &n)
		return n == 0
	})
	var earned int
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.loyalty_ledger WHERE source_id = $1 AND kind = 'earned'`, []any{mustUUID(str(pay["id"]))}, &earned)
	if b := engBalance(t, sa, aid); b != 125 || earned != 1 {
		t.Fatalf("idempotent per payment: balance %d, %d earn entries", b, earned)
	}
	if h := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+aid+"/ledger?filter[kind]=earned", nil).Items(); len(h) != 1 || h[0]["points"].(float64) != 125 {
		t.Fatalf("points ledger: %v", h)
	}

	// Liability = Σ positive balances × redemption value = Loyalty Liability Report (FR-LOY-08).
	var points int64
	sysQueryRow(t, inst, `SELECT coalesce(sum(balance) FILTER (WHERE balance > 0), 0) FROM crm.loyalty_accounts WHERE property_id = $1`, []any{inst.Main}, &points)
	liab := sa.Must(200, "GET", "/api/v1/crm/loyalty/liability", nil).JSON()
	if int64(liab["points"].(float64)) != points || dec(liab["liability"]).IntPart() != points*100 {
		t.Fatalf("liability %v for %d points", liab, points)
	}
	rep := engReport(t, sa, "crm.loyalty_liability", "params[to]="+engToday())
	var repPoints int64
	repTotal := dec("0")
	for _, r := range rep {
		repPoints += int64(r["points"].(float64))
		repTotal = repTotal.Add(dec(r["liability"]))
	}
	if repPoints != points || !repTotal.Equal(dec(liab["liability"])) {
		t.Fatalf("Loyalty Liability Report %d / %s vs %d / %v", repPoints, repTotal, points, liab["liability"])
	}
	if rows := engReport(t, sa, "crm.loyalty_points", "params[kind]=earned"); len(rows) == 0 {
		t.Fatal("Loyalty Points Report has no earned rows")
	}

	// A full refund reverses the 125 points (FR-LOY-04).
	sa.Must(201, "POST", "/api/v1/billing/refunds", map[string]any{"paymentId": pay["id"], "amount": "1250000", "reason": "Dinner cancelled"})
	engDispatch(t, "points reversed", func() bool { return engBalance(t, sa, aid) == 0 })
	if h := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+aid+"/ledger?filter[kind]=reversed", nil).Items(); len(h) != 1 || h[0]["points"].(float64) != -125 {
		t.Fatalf("reversal: %v", h)
	}

	// Adjust Points through approval (FR-LOY-09): reason required.
	if r := sa.Do("POST", "/api/v1/crm/loyalty/accounts/"+aid+":adjust", map[string]any{"points": 500}); r.Status != 422 {
		t.Fatalf("adjust without reason: %s", r)
	}
	adj := sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":adjust", map[string]any{"points": 600, "reason": "Goodwill: course closure"}).JSON()
	if adj["status"] != "approved" || engBalance(t, sa, aid) != 600 {
		t.Fatalf("adjustment without workflow is approved at once: %v", adj)
	}
	if l := sa.Must(200, "GET", "/api/v1/crm/loyalty/adjustments?filter[accountId]="+aid, nil).Items(); len(l) != 1 {
		t.Fatalf("adjustments: %v", l)
	}

	// Points as a tender on the folio (front desk, FR-LOY-05 / FR-OPS-P3-03): 50 points × Rp100 = Rp5.000.
	f2 := engFolio(t, sa, cust, "Lunch "+sfx, "300000")
	red := fd.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":redeem", map[string]any{"folioId": f2, "points": 50}, "Idempotency-Key", "loy-"+sfx).JSON()
	if dec(red["payment"].(map[string]any)["amount"]).IntPart() != 5000 || engBalance(t, sa, aid) != 550 {
		t.Fatalf("redeem 50 points: %v", red)
	}
	again := fd.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":redeem", map[string]any{"folioId": f2, "points": 50}, "Idempotency-Key", "loy-"+sfx).JSON()
	if str(again["payment"].(map[string]any)["paymentId"]) != str(red["payment"].(map[string]any)["paymentId"]) || engBalance(t, sa, aid) != 550 {
		t.Fatalf("redeem retried: %v", again)
	}
	if r := fd.Do("POST", "/api/v1/crm/loyalty/accounts/"+aid+":redeem", map[string]any{"folioId": f2, "points": 9000}); r.Status == 200 {
		t.Fatalf("redeeming more than the balance: %s", r)
	}

	// Rewards: redeem, fulfil, cancel (points back, stock back).
	rw := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/rewards", map[string]any{"code": "LW" + sfx, "name": "Range bucket " + sfx, "rewardType": "voucher",
		"pointsCost": 100, "stock": 2}))
	r1 := fd.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":redeem-reward", map[string]any{"rewardId": rw}).JSON()
	if r1["status"] != "pending" || r1["fulfilmentCode"] == nil || engBalance(t, sa, aid) != 450 {
		t.Fatalf("reward redemption: %v", r1)
	}
	fd.Must(200, "POST", "/api/v1/crm/loyalty/redemptions/"+str(r1["id"])+":complete", map[string]any{"note": "Handed over"})
	r2 := fd.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":redeem-reward", map[string]any{"rewardId": rw}).JSON()
	if r := fd.Do("POST", "/api/v1/crm/loyalty/accounts/"+aid+":redeem-reward", map[string]any{"rewardId": rw}); r.Status != 409 {
		t.Fatalf("reward out of stock: %s", r)
	}
	fd.Must(200, "POST", "/api/v1/crm/loyalty/redemptions/"+str(r2["id"])+":cancel", map[string]any{"reason": "Changed mind"})
	if engBalance(t, sa, aid) != 450 {
		t.Fatalf("cancelled reward gives the points back: %d", engBalance(t, sa, aid))
	}
	if l := sa.Must(200, "GET", "/api/v1/crm/loyalty/redemptions?filter[accountId]="+aid, nil).Items(); len(l) != 2 {
		t.Fatalf("redemptions: %v", l)
	}

	// Account status: a suspended account cannot redeem.
	sa.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":set-status", map[string]any{"status": "suspended", "reason": "Fraud check"})
	if r := fd.Do("POST", "/api/v1/crm/loyalty/accounts/"+aid+":redeem-reward", map[string]any{"rewardId": rw}); r.Status != 409 {
		t.Fatalf("suspended account redeems: %s", r)
	}
	sa.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":set-status", map[string]any{"status": "active", "reason": "Cleared"})

	// Member referral reward (crm.opportunity_won with referrerCustomerId): once per opportunity.
	sa.Must(201, "POST", "/api/v1/crm/loyalty/earning-rules", map[string]any{"code": "LF" + sfx, "name": "Referral", "ruleType": "activity",
		"activity": "referral", "points": 250})
	var refPoints int64
	sysQueryRow(t, inst, `SELECT coalesce(sum(points), 0) FROM crm.loyalty_earning_rules WHERE property_id = $1 AND rule_type = 'activity'
		AND activity = 'referral' AND status = 'active' AND archived_at IS NULL`, []any{inst.Main}, &refPoints)
	opp := uuid.NewString()
	won := map[string]any{"opportunityId": opp, "number": "OPP-" + sfx, "quotationId": uuid.NewString(), "quotationNumber": "QUO-" + sfx,
		"customerId": uuid.NewString(), "line": "wedding", "value": "150000000", "currency": "IDR", "referrerCustomerId": cust}
	engPublish(t, "crm.opportunity_won", "crm.opportunity", won)
	engPublish(t, "crm.opportunity_won", "crm.opportunity", won)
	engDispatch(t, "referral reward", func() bool { return engBalance(t, sa, aid) == 450+refPoints })
	var refs int
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.loyalty_ledger WHERE source_id = $1`, []any{mustUUID(opp)}, &refs)
	if refs != 1 {
		t.Fatalf("referral reward once per opportunity: %d", refs)
	}

	// Tier evaluation: unlocked, the points of the period reach Gold (min 400).
	sa.Must(200, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":set-tier", map[string]any{"tierId": base, "lock": false, "reason": "Evaluate normally"})
	// Points earned in the window (earned − reversed + activity): an event attendance adds more.
	sa.Must(201, "POST", "/api/v1/crm/loyalty/earning-rules", map[string]any{"code": "LE" + sfx, "name": "Event", "ruleType": "activity",
		"activity": "event_attended", "points": 500})
	engPublish(t, "banquet.event_guest_checked_in", "banquet.event", map[string]any{"eventId": uuid.NewString(), "customerId": cust, "number": "EVT-" + sfx})
	engDispatch(t, "event points", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM crm.loyalty_ledger WHERE account_id = $1 AND source_type = 'banquet.event'`, []any{mustUUID(aid)}, &n)
		return n == 1
	})
	ev := sa.Must(200, "POST", "/api/v1/crm/loyalty/tiers:evaluate", nil).JSON()
	if a := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+aid, nil).JSON(); str(a["tierId"]) != gold || ev["changed"].(float64) < 1 {
		t.Fatalf("tier evaluation to Gold: %v (%v)", a["tierId"], ev)
	}

	// Expiry (FR-LOY-06): imported opening points valid until yesterday expire; the notice goes out before.
	exp := customer(t, sa, "LOX"+sfx, "Expiring Eka "+sfx, map[string]any{"email": "eka" + sfx + "@loy.test"})
	yesterday := time.Now().In(clubLoc(inst)).AddDate(0, 0, -1).Format("2006-01-02")
	soon := time.Now().In(clubLoc(inst)).AddDate(0, 0, 10).Format("2006-01-02")
	csv := "customerCode,points,expiresOn,reference\nLOX" + sfx + ",200," + yesterday + ",RH-1\nLOX" + sfx + ",80," + soon + ",RH-2\n"
	bad := sa.Must(200, "POST", "/api/v1/crm/loyalty/opening-balances:import", map[string]any{"mode": "commit", "csv": csv + "NOPE" + sfx + ",10,,\n"}).JSON()
	if bad["mode"] != "preview" || len(bad["errors"].([]any)) != 1 {
		t.Fatalf("import with a bad row saves nothing: %v", bad)
	}
	ok := sa.Must(200, "POST", "/api/v1/crm/loyalty/opening-balances:import", map[string]any{"mode": "commit", "csv": csv, "filename": "loyalty.csv"}).JSON()
	if ok["imported"].(float64) != 2 || ok["points"].(float64) != 280 {
		t.Fatalf("opening balance import (one lot per reference): %v", ok)
	}
	if re := sa.Must(200, "POST", "/api/v1/crm/loyalty/opening-balances:import", map[string]any{"mode": "commit", "csv": csv}).JSON(); re["skipped"].(float64) != 2 {
		t.Fatalf("re-import: %v", re)
	}
	var expAcct string
	sysQueryRow(t, inst, `SELECT id::text FROM crm.loyalty_accounts WHERE customer_id = $1`, []any{mustUUID(exp)}, &expAcct)
	res, err := inst.App.Engage.Loyalty.RunDaily(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r := res[inst.Main]; r.Points < 200 || r.Notified < 1 || engBalance(t, sa, expAcct) != 80 {
		t.Fatalf("expiry: %+v balance %d", r, engBalance(t, sa, expAcct))
	}

	// Customer 360 Loyalty section (FR-C360-01).
	v := sa.Must(200, "GET", "/api/v1/crm/customers/"+cust+"/360", nil).JSON()
	if sec, _ := v["sections"].(map[string]any)["loyalty"].(map[string]any); sec == nil || sec["enrolled"] != true {
		t.Fatalf("Customer 360 loyalty: %v", v["sections"])
	}
	if v["placeholders"].(map[string]any)["loyalty"] != "live" {
		t.Fatalf("loyalty placeholder: %v", v["placeholders"])
	}

	// Accounting export rows: loyalty movements and the liability at the end of the day (FR-INT-P3-05).
	if x := accountingExport(t, sa); !strings.Contains(x, "loyalty") || !strings.Contains(x, "loyalty_points") {
		t.Fatalf("accounting export misses loyalty rows: %s", x)
	}
}

// FR-APP-P3-01 Member App Loyalty: join, My Points, Tier, Rewards, Points
// History, redeem a reward and pay a folio with points.
func TestP3EngagementMemberLoyalty(t *testing.T) {
	sa := superAdmin(t, inst)
	mc := roleUser(t, inst, "member")
	me := memberCustomer(t, sa)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	home := mc.Must(200, "GET", "/api/v1/member/loyalty", nil).JSON()
	if home["redemptionValue"] == nil {
		t.Fatalf("my loyalty: %v", home)
	}
	if home["enrolled"] != true {
		home = mc.Must(200, "POST", "/api/v1/member/loyalty:join", nil).JSON()
	}
	acct := home["account"].(map[string]any)
	if home["enrolled"] != true || acct["customerId"] != me {
		t.Fatalf("join: %v", home)
	}
	aid := str(acct["id"])
	sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":adjust", map[string]any{"points": 1000, "reason": "Welcome bonus " + sfx})
	if ts := mc.Must(200, "GET", "/api/v1/member/loyalty/tiers", nil).Items(); len(ts) == 0 {
		t.Fatal("tiers")
	}
	rw := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/rewards", map[string]any{"code": "MW" + sfx, "name": "Cap " + sfx, "pointsCost": 200}))
	rs := mc.Must(200, "GET", "/api/v1/member/loyalty/rewards", nil).Items()
	if !containsID(rs, rw) {
		t.Fatalf("rewards: %v", rs)
	}
	got := mc.Must(201, "POST", "/api/v1/member/loyalty/rewards/"+rw+":redeem", map[string]any{"quantity": 1}).JSON()
	if got["channel"] != "member_app" || got["points"].(float64) != 200 {
		t.Fatalf("member reward: %v", got)
	}
	if l := mc.Must(200, "GET", "/api/v1/member/loyalty/redemptions", nil).Items(); !containsID(l, str(got["id"])) {
		t.Fatalf("my redemptions: %v", l)
	}
	folio := engFolio(t, sa, me, "Pro shop "+sfx, "50000")
	paid := mc.Must(200, "POST", "/api/v1/member/folios/"+folio+":pay-with-points", map[string]any{"points": 100}, "Idempotency-Key", newKey()).JSON()
	if dec(paid["payment"].(map[string]any)["amount"]).IntPart() != 10000 {
		t.Fatalf("pay with points: %v", paid)
	}
	other := customer(t, sa, "MLX"+sfx, "Someone Else "+sfx, nil)
	of := engFolio(t, sa, other, "Not mine "+sfx, "50000")
	mc.Must(404, "POST", "/api/v1/member/folios/"+of+":pay-with-points", map[string]any{"points": 10})
	h := mc.Must(200, "GET", "/api/v1/member/loyalty/history", nil).Items()
	kinds := map[string]bool{}
	for _, e := range h {
		kinds[str(e["kind"])] = true
	}
	if !kinds["adjusted"] || !kinds["redeemed"] {
		t.Fatalf("points history: %v", h)
	}
	// Top Spender and staff loyalty screens stay internal (FR-TOP-06).
	mc.Must(403, "GET", "/api/v1/crm/top-spenders", nil)
	mc.Must(403, "GET", "/api/v1/crm/loyalty/accounts", nil)
}

// engCampaignDone dispatches the due campaign batches until the campaign is sent.
func engCampaignDone(t *testing.T, sa *Client, campaign string) map[string]any {
	t.Helper()
	for i := 0; i < 20; i++ {
		if _, err := inst.App.Engage.Service.DispatchDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		c := sa.Must(200, "GET", "/api/v1/crm/campaigns/"+campaign+"/detail", nil).JSON()
		if c["status"] == "sent" {
			return c
		}
	}
	t.Fatalf("campaign %s not sent", campaign)
	return nil
}

// engIDs runs a query returning text values.
func engIDs(t *testing.T, sql string, args ...any) []string {
	t.Helper()
	var out []string
	err := inst.DB.WithReadTx(dbtx.System(context.Background()), func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), sql, args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// engRecipientStatus maps customer → recipient status of a campaign.
func engRecipientStatus(t *testing.T, campaign string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, row := range engIDs(t, `SELECT customer_id::text || '|' || status FROM crm.campaign_recipients WHERE campaign_id = $1`, mustUUID(campaign)) {
		p := strings.SplitN(row, "|", 2)
		out[p[0]] = p[1]
	}
	return out
}

// EP-06 acceptance: a WhatsApp campaign to a segment of 2.000 customers
// sends only to those who opted in on WhatsApp; a recipient who
// unsubscribes does not receive the next campaign. Also: approval above
// the recipient threshold, throttled batches, unique promo codes published
// for Commercial (crm.campaign_sent), suppression list, preview, tracked
// link, conversion by payment and by promo code, cancel, FR-LEAD-09 (a
// lead without marketing consent never receives a campaign), static and
// dynamic segments, communication preferences (staff and Member App),
// birthday reminders, Customer 360 and Corporate 360.
func TestP3EngagementCampaigns(t *testing.T) {
	sa := superAdmin(t, inst)
	mk := roleUser(t, inst, "marketing_staff")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	prefix := "CMP" + sfx + "-"
	sysExec(t, inst, `INSERT INTO crm.customers (id, property_id, code, name, phone, customer_type, status)
		SELECT gen_random_uuid(), $1, $2 || lpad(g::text, 4, '0'), 'Campaign Guest ' || g, '+62877' || lpad(g::text, 7, '0'), 'individual', 'active'
		FROM generate_series(1, 2000) g`, inst.Main, prefix)
	sysExec(t, inst, `INSERT INTO crm.communication_preferences (customer_id, channel, property_id, opted_in, source)
		SELECT id, 'whatsapp', property_id, true, 'import' FROM crm.customers WHERE code LIKE $1 || '%' AND substring(code from length($1) + 1)::int <= 600`, prefix)
	ids := engIDs(t, `SELECT id::text FROM crm.customers WHERE code LIKE $1 || '%' ORDER BY code`, prefix)
	optedIn := map[string]bool{}
	for _, id := range engIDs(t, `SELECT p.customer_id::text FROM crm.communication_preferences p JOIN crm.customers c ON c.id = p.customer_id
		WHERE c.code LIKE $1 || '%' AND p.opted_in`, prefix) {
		optedIn[id] = true
	}
	if len(ids) != 2000 || len(optedIn) != 600 {
		t.Fatalf("fixture: %d customers, %d opted in", len(ids), len(optedIn))
	}

	// Static segment of the 2.000 customers.
	seg := idOf(mk.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "SEG" + sfx, "name": "Campaign test " + sfx, "segmentType": "static"}))
	if r := mk.Must(200, "POST", "/api/v1/crm/segments/"+seg+"/members:add", map[string]any{"customerIds": ids}).JSON(); r["memberCount"].(float64) != 2000 {
		t.Fatalf("static segment members: %v", r)
	}
	mk.Must(200, "POST", "/api/v1/crm/segments/"+seg+"/members:remove", map[string]any{"customerIds": []string{ids[1999]}})
	mk.Must(200, "POST", "/api/v1/crm/segments/"+seg+"/members:add", map[string]any{"customerIds": []string{ids[1999]}})

	camp := mk.Must(201, "POST", "/api/v1/crm/campaigns", map[string]any{"code": "WA" + sfx, "name": "Birthday month " + sfx, "segmentId": seg,
		"channel": "whatsapp", "templateEvent": "crm.campaign_message", "subject": "Birthday month", "body": "Halo {{.name}}, pakai kode {{.promoCode}}: {{.link}}",
		"promoMode": "unique", "promoCode": "bday" + sfx, "targetUrl": "https://club.test/offer"})
	cid := idOf(camp)
	pv := mk.Must(200, "GET", "/api/v1/crm/campaigns/"+cid+"/preview?customerId="+ids[0], nil).JSON()
	if pv["eligible"].(float64) != 600 || pv["skipped"].(map[string]any)["no_consent"].(float64) != 1400 || !strings.Contains(str(pv["body"]), "BDAY"+sfx+"-") {
		t.Fatalf("preview: %v", pv)
	}
	// More than 1.000 recipients: approval first (no workflow ⇒ approved at once).
	sch := mk.Must(200, "POST", "/api/v1/crm/campaigns/"+cid+":schedule", map[string]any{}).JSON()
	if sch["status"] != "scheduled" || sch["approvalRequestId"] == nil {
		t.Fatalf("schedule above the approval threshold: %v", sch)
	}
	if r := mk.Do("PATCH", "/api/v1/crm/campaigns/"+cid, map[string]any{"name": "Too late"}); r.Status != 409 {
		t.Fatalf("a scheduled campaign is no longer editable: %s", r)
	}
	engCampaignDone(t, sa, cid)
	st := mk.Must(200, "GET", "/api/v1/crm/campaigns/"+cid+"/stats", nil).JSON()
	if st["sent"].(float64) != 600 || st["skipped"].(map[string]any)["no_consent"].(float64) != 1400 {
		t.Fatalf("AC: only WhatsApp opt-ins receive the campaign: %v", st)
	}
	for c, s := range engRecipientStatus(t, cid) {
		if (s == "sent") != optedIn[c] {
			t.Fatalf("customer %s (opted in %v) is %s", c, optedIn[c], s)
		}
	}
	var wa int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notification_deliveries d JOIN crm.campaign_recipients r ON r.token = d.payload->>'trackingToken'
		WHERE r.campaign_id = $1 AND d.channel = 'whatsapp'`, []any{mustUUID(cid)}, &wa)
	if wa != 600 {
		t.Fatalf("WhatsApp messages queued through the integration layer: %d", wa)
	}
	// Commercial issues the personal promo codes from crm.campaign_sent (batches of the Campaign Policies).
	var published, batches int
	sysQueryRow(t, inst, `SELECT coalesce(sum(jsonb_array_length(payload->'recipients')), 0)::int, count(*)::int FROM platform.outbox
		WHERE event_type = 'crm.campaign_sent' AND aggregate_id = $1`, []any{mustUUID(cid)}, &published, &batches)
	if published != 600 || batches < 2 {
		t.Fatalf("crm.campaign_sent recipients %d in %d batches", published, batches)
	}
	if rs := mk.Must(200, "GET", "/api/v1/crm/campaigns/"+cid+"/recipients?filter[status]=sent&limit=5", nil).Items(); len(rs) != 5 ||
		!strings.HasPrefix(str(rs[0]["promoCode"]), "BDAY"+sfx+"-") {
		t.Fatalf("recipients: %v", rs)
	}

	// Tracked link, unsubscribe, conversions.
	tok := func(customer string) string {
		var tk string
		sysQueryRow(t, inst, `SELECT token FROM crm.campaign_recipients WHERE campaign_id = $1 AND customer_id = $2`, []any{mustUUID(cid), mustUUID(customer)}, &tk)
		return tk
	}
	noFollow := anon(t, inst)
	noFollow.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if r := noFollow.Do("GET", "/api/v1/public/campaign-links/"+tok(ids[1]), nil); r.Status != 302 || r.Header.Get("Location") != "https://club.test/offer" {
		t.Fatalf("tracked link: %s %v", r, r.Header)
	}
	pub := anon(t, inst)
	if u := pub.Must(200, "GET", "/api/v1/public/unsubscribe/"+tok(ids[0]), nil).JSON(); u["status"] != "subscribed" || strings.Contains(str(u["address"]), "877") {
		t.Fatalf("unsubscribe page (masked): %v", u)
	}
	if u := pub.Must(200, "POST", "/api/v1/public/unsubscribe/"+tok(ids[0]), map[string]any{}).JSON(); u["status"] != "unsubscribed" {
		t.Fatalf("unsubscribe: %v", u)
	}
	f := engFolio(t, sa, ids[2], "Campaign dinner "+sfx, "400000")
	sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": f, "amount": "400000", "methodType": "cash", "channel": "venue"})
	var code3 string
	sysQueryRow(t, inst, `SELECT promo_code FROM crm.campaign_recipients WHERE campaign_id = $1 AND customer_id = $2`, []any{mustUUID(cid), mustUUID(ids[3])}, &code3)
	engPublish(t, "commercial.promotion_applied", "commercial.promotion", map[string]any{"promotionId": uuid.NewString(), "code": code3,
		"sourceType": "pos_order", "sourceId": uuid.NewString(), "customerId": ids[3], "businessLine": "pos", "discount": "50000", "currency": "IDR"})
	engDispatch(t, "conversions", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*)::int FROM crm.campaign_recipients WHERE campaign_id = $1 AND converted_at IS NOT NULL`, []any{mustUUID(cid)}, &n)
		return n == 2
	})
	st = mk.Must(200, "GET", "/api/v1/crm/campaigns/"+cid+"/stats", nil).JSON()
	if st["clicked"].(float64) != 1 || st["converted"].(float64) != 2 || st["unsubscribed"].(float64) != 1 {
		t.Fatalf("tracking: %v", st)
	}

	// The next campaign skips the unsubscribed recipient and the suppressed number.
	var phone4 string
	sysQueryRow(t, inst, `SELECT phone FROM crm.customers WHERE id = $1`, []any{mustUUID(ids[4])}, &phone4)
	mk.Must(201, "POST", "/api/v1/crm/suppressions", map[string]any{"channel": "whatsapp", "address": phone4, "reason": "BSP: number blocked"})
	c2 := idOf(mk.Must(201, "POST", "/api/v1/crm/campaigns", map[string]any{"code": "WB" + sfx, "name": "Weekend " + sfx, "segmentId": seg,
		"channel": "whatsapp", "templateEvent": "crm.campaign_message", "subject": "Weekend", "body": "Halo {{.name}}"}))
	mk.Must(200, "POST", "/api/v1/crm/campaigns/"+c2+":schedule", map[string]any{})
	engCampaignDone(t, sa, c2)
	s2 := engRecipientStatus(t, c2)
	if s2[ids[0]] != "skipped_no_consent" || s2[ids[4]] != "skipped_suppressed" || s2[ids[5]] != "sent" {
		t.Fatalf("AC: the unsubscribed recipient gets no further campaign: %s / %s / %s", s2[ids[0]], s2[ids[4]], s2[ids[5]])
	}
	if rep := engReport(t, sa, "crm.campaign_performance", "params[channel]=whatsapp"); len(rep) < 2 {
		t.Fatalf("Campaign Performance Report: %v", rep)
	}
	c3 := idOf(mk.Must(201, "POST", "/api/v1/crm/campaigns", map[string]any{"code": "WC" + sfx, "name": "Draft " + sfx, "segmentId": seg,
		"channel": "email", "templateEvent": "crm.campaign_message", "body": "Hi"}))
	if c := mk.Must(200, "POST", "/api/v1/crm/campaigns/"+c3+":cancel", map[string]any{"reason": "Wrong segment"}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancel: %v", c)
	}

	// FR-LEAD-09: a lead converted without marketing consent never receives a campaign.
	lead := sa.Must(201, "POST", "/api/v1/crm/leads", map[string]any{"name": "No Consent Lead " + sfx, "email": "nolead" + sfx + "@cmp.test",
		"phone": "+62899" + sfx, "source": "walk_in", "line": "golf", "duplicateAcknowledged": true}).JSON()
	conv := sa.Must(200, "POST", "/api/v1/crm/leads/"+str(lead["id"])+":convert", map[string]any{"createOpportunity": false}).JSON()
	noConsent := str(conv["customerId"])
	yes := customer(t, sa, "CMY"+sfx, "Consenting Citra "+sfx, map[string]any{"email": "citra" + sfx + "@cmp.test", "marketingOptIn": true,
		"birthDate": time.Now().In(clubLoc(inst)).AddDate(-35, 0, 0).Format("2006-01-02")})
	seg2 := idOf(mk.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "SEL" + sfx, "name": "Email " + sfx, "segmentType": "static"}))
	mk.Must(200, "POST", "/api/v1/crm/segments/"+seg2+"/members:add", map[string]any{"customerIds": []string{noConsent, yes}})
	c4 := idOf(mk.Must(201, "POST", "/api/v1/crm/campaigns", map[string]any{"code": "EM" + sfx, "name": "Newsletter " + sfx, "segmentId": seg2,
		"channel": "email", "templateEvent": "crm.campaign_message", "subject": "News", "body": "Hi {{.name}}", "promoMode": "shared", "promoCode": "news" + sfx}))
	mk.Must(200, "POST", "/api/v1/crm/campaigns/"+c4+":schedule", map[string]any{})
	engCampaignDone(t, sa, c4)
	if s4 := engRecipientStatus(t, c4); s4[noConsent] != "skipped_no_consent" || s4[yes] != "sent" {
		t.Fatalf("FR-LEAD-09: %v", s4)
	}

	// Dynamic segment with P3 dimensions (refreshed).
	dyn := idOf(mk.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "DYN" + sfx, "name": "Responders " + sfx,
		"rules": map[string]any{"tags": []string{"campaign_responder"}, "lookbackDays": 30}}))
	if r := mk.Must(200, "POST", "/api/v1/crm/segments/"+dyn+":refresh", nil).JSON(); r["memberCount"].(float64) < 3 || r["segmentType"] != "dynamic" {
		t.Fatalf("dynamic segment with P3 tags: %v", r)
	}

	// Communication preferences: staff (with history) and the Member App (FR-APP-P3-08).
	cp := sa.Must(200, "POST", "/api/v1/crm/customers/"+yes+"/communication-preferences", map[string]any{"whatsapp": true}).JSON()
	if !engChannel(cp, "whatsapp") || !engChannel(cp, "email") {
		t.Fatalf("preferences: %v", cp)
	}
	if h := sa.Must(200, "GET", "/api/v1/crm/customers/"+yes+"/consent-history", nil).Items(); len(h) != 1 || h[0]["channel"] != "whatsapp" {
		t.Fatalf("consent history: %v", h)
	}
	mc := roleUser(t, inst, "member")
	memberCustomer(t, sa)
	mine := mc.Must(200, "POST", "/api/v1/member/communication-preferences", map[string]any{"email": false, "whatsapp": true, "inApp": true}).JSON()
	if engChannel(mine, "email") || !engChannel(mine, "whatsapp") {
		t.Fatalf("member preferences: %v", mine)
	}
	if g := mc.Must(200, "GET", "/api/v1/member/communication-preferences", nil).JSON(); len(g["channels"].([]any)) != 3 {
		t.Fatalf("member preferences: %v", g)
	}

	// Birthday reminder (FR-CMP-06): sent once per year and customer.
	rule := idOf(mk.Must(201, "POST", "/api/v1/crm/reminder-rules", map[string]any{"code": "BD" + sfx, "name": "Birthday " + sfx, "kind": "birthday",
		"channel": "email", "promoCode": "hbd" + sfx}))
	if r := mk.Do("POST", "/api/v1/crm/reminder-rules", map[string]any{"code": "RN" + sfx, "name": "Renewal", "kind": "renewal", "daysOffset": 30}); r.Status != 422 {
		t.Fatalf("renewal H-30 is sent by Membership: %s", r)
	}
	if p := mk.Must(200, "GET", "/api/v1/crm/reminder-rules/"+rule+"/preview", nil).JSON(); !engHas(p["customers"], yes, "queued") {
		t.Fatalf("reminder preview: %v", p)
	}
	for i := 0; i < 2; i++ {
		if _, err := inst.App.Engage.Service.RunReminders(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if l := mk.Must(200, "GET", "/api/v1/crm/reminder-log?filter[ruleId]="+rule+"&filter[customerId]="+yes, nil).Items(); len(l) != 1 || l[0]["status"] != "sent" {
		t.Fatalf("reminder log: %v", l)
	}

	// Customer 360 and Corporate 360 (FR-C360-01/04/05).
	v := sa.Must(200, "GET", "/api/v1/crm/customers/"+yes+"/360", nil).JSON()
	secs := v["sections"].(map[string]any)
	for _, k := range []string{"loyalty", "campaignResponse", "complaints", "sales"} {
		if secs[k] == nil {
			t.Fatalf("Customer 360 section %s missing: %v", k, secs)
		}
	}
	if cr := secs["campaignResponse"].(map[string]any); len(cr["recent"].([]any)) == 0 {
		t.Fatalf("campaign section: %v", cr)
	}
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "CO" + sfx, "name": "PT Engage " + sfx}))
	sa.Must(201, "POST", "/api/v1/crm/corporate-nominees", map[string]any{"corporateAccountId": corp, "customerId": yes, "title": "Director"})
	c360 := sa.Must(200, "GET", "/api/v1/crm/corporate-accounts/"+corp+"/360", nil).JSON()
	if len(c360["nominees"].([]any)) != 1 || c360["billing"] == nil {
		t.Fatalf("Corporate 360: %v", c360)
	}
}

func engChannel(p map[string]any, ch string) bool {
	for _, c := range p["channels"].([]any) {
		m := c.(map[string]any)
		if m["channel"] == ch {
			return m["optedIn"] == true
		}
	}
	return false
}

func engHas(list any, customer, status string) bool {
	l, _ := list.([]any)
	for _, x := range l {
		m := x.(map[string]any)
		if m["customerId"] == customer && m["status"] == status {
			return true
		}
	}
	return false
}

// engEvents returns the history kinds of a ticket.
func engEvents(d map[string]any) []map[string]any {
	var out []map[string]any
	for _, e := range d["events"].([]any) {
		out = append(out, e.(map[string]any))
	}
	return out
}

// EP-07 acceptance: a High priority ticket not answered within its SLA is
// escalated automatically to the line manager and the escalation is in the
// ticket history. Also: categories with default priority and line, the
// workflow Open → In Progress → Resolved → Closed with reopen, customer
// communication, second-level escalation to the General Manager,
// compensation through approval (loyalty points), tickets from low
// feedback (manual and automatic), the Member App Support (complaints,
// conversation, reopen, feedback, NPS), the website complaint form, NPS per
// business line and the Complaint and NPS Reports.
func TestP3EngagementComplaints(t *testing.T) {
	sa := superAdmin(t, inst)
	fd := roleUser(t, inst, "front_desk")
	gm := roleUser(t, inst, "golf_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	cust := customer(t, sa, "TKT"+sfx, "Complaining Kevin "+sfx, map[string]any{"email": "kevin" + sfx + "@tkt.test"})
	cat := idOf(sa.Must(201, "POST", "/api/v1/crm/ticket-categories", map[string]any{"code": "TC" + sfx, "name": "Course " + sfx,
		"businessLine": "golf", "defaultPriority": "high"}))

	// AC: High priority, first response SLA passed → escalated to the golf line manager.
	tk := fd.Must(201, "POST", "/api/v1/crm/tickets", map[string]any{"customerId": cust, "categoryId": cat, "channel": "phone",
		"subject": "Bunker not raked", "description": "Hole 7 bunkers were not raked this morning"}).JSON()
	tid := str(tk["id"])
	if tk["priority"] != "high" || tk["businessLine"] != "golf" || tk["status"] != "open" || tk["number"] == nil {
		t.Fatalf("ticket from category defaults: %v", tk)
	}
	sysExec(t, inst, `UPDATE crm.tickets SET first_response_due_at = now() - interval '1 minute' WHERE id = $1`, mustUUID(tid))
	if _, err := inst.App.Engage.Service.RunSLA(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := gm.Must(200, "GET", "/api/v1/crm/tickets/"+tid, nil).JSON()
	esc := false
	for _, e := range engEvents(d) {
		if e["kind"] == "escalated" {
			det := e["details"].(map[string]any)
			esc = det["role"] == "golf_manager" && det["notified"].(float64) >= 1 && det["level"].(float64) == 1
		}
	}
	if d["status"] != "escalated" || d["escalationLevel"].(float64) != 1 || !esc {
		t.Fatalf("AC: escalation to the line manager in the history: %v", d)
	}
	var notified int
	sysQueryRow(t, inst, `SELECT count(*)::int FROM platform.notifications WHERE user_id = $1 AND link LIKE '%' || $2`, []any{mustUUID(userID(t, "golf_manager")), tid}, &notified)
	if notified < 1 {
		t.Fatal("the golf manager was not notified")
	}
	if l := gm.Must(200, "GET", "/api/v1/crm/tickets?overdue=true&filter[businessLine]=golf", nil).Items(); !containsID(l, tid) {
		t.Fatalf("overdue tickets: %v", l)
	}
	// Still unresolved after the second-escalation window → General Manager.
	sysExec(t, inst, `UPDATE crm.tickets SET escalated_at = now() - interval '1 day' WHERE id = $1`, mustUUID(tid))
	if _, err := inst.App.Engage.Service.RunSLA(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := sa.Must(200, "GET", "/api/v1/crm/tickets/"+tid, nil).JSON(); d["escalationLevel"].(float64) != 2 {
		t.Fatalf("second escalation: %v", d)
	}
	if r := gm.Do("POST", "/api/v1/crm/tickets/"+tid+":escalate", map[string]any{"reason": "Again"}); r.Status != 409 {
		t.Fatalf("escalation beyond the General Manager: %s", r)
	}

	// Workflow with customer communication.
	gmID := userID(t, "golf_manager")
	gm.Must(200, "POST", "/api/v1/crm/tickets/"+tid+":assign", map[string]any{"assignedTo": gmID})
	gm.Must(200, "POST", "/api/v1/crm/tickets/"+tid+":comment", map[string]any{"body": "Ground staff informed", "internal": true})
	c := gm.Must(200, "POST", "/api/v1/crm/tickets/"+tid+":comment", map[string]any{"body": "We are sorry — the bunkers are raked now."}).JSON()
	if c["firstRespondedAt"] == nil {
		t.Fatalf("first response: %v", c)
	}
	gm.Must(200, "POST", "/api/v1/crm/tickets/"+tid+":resolve", map[string]any{"resolution": "Bunkers raked, crew briefed"})
	gm.Must(200, "POST", "/api/v1/crm/tickets/"+tid+":close", map[string]any{"reason": "Customer satisfied"})
	if r := gm.Must(200, "POST", "/api/v1/crm/tickets/"+tid+":reopen", map[string]any{"reason": "Happened again"}).JSON(); r["status"] != "open" || r["reopenedCount"].(float64) != 1 {
		t.Fatalf("reopen: %v", r)
	}
	// Compensation through approval: loyalty points (FR-TKT-05).
	acct := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": cust}))
	comp := gm.Must(201, "POST", "/api/v1/crm/tickets/"+tid+":compensate", map[string]any{"type": "points", "points": 300, "description": "Apology points"}).JSON()
	if comp["status"] != "approved" || comp["ledgerId"] == nil || engBalance(t, sa, acct) != 300 {
		t.Fatalf("points compensation: %v", comp)
	}
	gm.Must(201, "POST", "/api/v1/crm/tickets/"+tid+":compensate", map[string]any{"type": "voucher", "amount": "150000", "description": "F&B voucher"})
	if d := gm.Must(200, "GET", "/api/v1/crm/tickets/"+tid, nil).JSON(); len(d["compensations"].([]any)) != 2 {
		t.Fatalf("compensations on the ticket: %v", d["compensations"])
	}
	other := fd.Must(201, "POST", "/api/v1/crm/tickets", map[string]any{"contactName": "Walk-in Guest", "contactPhone": "0812" + sfx, "priority": "low",
		"subject": "Parking", "description": "Parking was full"}).JSON()
	fd.Must(403, "POST", "/api/v1/crm/tickets/"+str(other["id"])+":escalate", map[string]any{"reason": "x"})
	if e := gm.Must(200, "POST", "/api/v1/crm/tickets/"+str(other["id"])+":escalate", map[string]any{"reason": "VIP guest"}).JSON(); e["status"] != "escalated" {
		t.Fatalf("manual escalation: %v", e)
	}

	// Tickets from low feedback (FR-TKT-01): manual and automatic.
	inv := sa.Must(201, "POST", "/api/v1/crm/feedback-requests", map[string]any{"customerId": cust, "contextType": "round", "contextLabel": "Round " + sfx}).JSON()
	fb := anon(t, inst).Must(201, "POST", "/api/v1/public/feedback/"+str(inv["token"]), map[string]any{"rating": 1, "comment": "Slow play"}).JSON()
	ft := fd.Must(200, "POST", "/api/v1/crm/feedback/"+str(fb["id"])+":open-ticket", map[string]any{}).JSON()
	if ft["created"] != true || ft["ticket"].(map[string]any)["channel"] != "feedback" {
		t.Fatalf("ticket from feedback: %v", ft)
	}
	if again := fd.Must(200, "POST", "/api/v1/crm/feedback/"+str(fb["id"])+":open-ticket", map[string]any{}).JSON(); again["created"] != false {
		t.Fatalf("one ticket per feedback: %v", again)
	}
	inv2 := sa.Must(201, "POST", "/api/v1/crm/feedback-requests", map[string]any{"customerId": cust, "contextType": "fnb", "contextLabel": "Lunch " + sfx}).JSON()
	fb2 := anon(t, inst).Must(201, "POST", "/api/v1/public/feedback/"+str(inv2["token"]), map[string]any{"rating": 2, "nps": 4}).JSON()
	if _, err := inst.App.Engage.Service.RunSLA(context.Background()); err != nil {
		t.Fatal(err)
	}
	var auto int
	sysQueryRow(t, inst, `SELECT count(*)::int FROM crm.tickets WHERE feedback_id = $1`, []any{mustUUID(str(fb2["id"]))}, &auto)
	if auto != 1 {
		t.Fatal("low feedback opens a ticket automatically (Complaint Policies)")
	}

	// Member App Support (FR-APP-P3-06).
	mc := roleUser(t, inst, "member")
	memberCustomer(t, sa)
	if cats := mc.Must(200, "GET", "/api/v1/member/ticket-categories", nil).Items(); !containsID(cats, cat) {
		t.Fatalf("member categories: %v", cats)
	}
	mt := mc.Must(201, "POST", "/api/v1/member/tickets", map[string]any{"categoryId": cat, "subject": "Locker broken", "description": "Locker 12 does not close"}).JSON()
	mid := str(mt["id"])
	if mt["channel"] != "member_app" || mt["assignedTo"] != nil {
		t.Fatalf("member complaint: %v", mt)
	}
	gm.Must(200, "POST", "/api/v1/crm/tickets/"+mid+":comment", map[string]any{"body": "internal: call locker vendor", "internal": true})
	gm.Must(200, "POST", "/api/v1/crm/tickets/"+mid+":comment", map[string]any{"body": "We will fix it today"})
	md := mc.Must(200, "GET", "/api/v1/member/tickets/"+mid, nil).JSON()
	for _, e := range engEvents(md) {
		if e["internal"] == true || strings.Contains(str(e["body"]), "internal:") {
			t.Fatalf("internal notes are hidden from the member: %v", e)
		}
	}
	mc.Must(200, "POST", "/api/v1/member/tickets/"+mid+":reply", map[string]any{"body": "Thanks!"})
	gm.Must(200, "POST", "/api/v1/crm/tickets/"+mid+":resolve", map[string]any{"resolution": "Lock replaced"})
	if r := mc.Must(200, "POST", "/api/v1/member/tickets/"+mid+":reopen", map[string]any{"reason": "Still broken"}).JSON(); r["status"] != "open" {
		t.Fatalf("member reopen: %v", r)
	}
	if l := mc.Must(200, "GET", "/api/v1/member/tickets", nil).Items(); !containsID(l, mid) {
		t.Fatalf("my complaints: %v", l)
	}
	mc.Must(404, "GET", "/api/v1/member/tickets/"+tid, nil)
	if f := mc.Must(201, "POST", "/api/v1/member/support/feedback", map[string]any{"contextType": "fnb", "rating": 2, "comment": "Cold soup"}).JSON(); f["lowScore"] != true {
		t.Fatalf("member feedback: %v", f)
	}
	mc.Must(201, "POST", "/api/v1/member/nps", map[string]any{"score": 9, "businessLine": "golf", "comment": "Great course"})
	sa.Must(201, "POST", "/api/v1/crm/nps-responses", map[string]any{"customerId": cust, "score": 3, "businessLine": "golf", "comment": "Slow"})
	nps := sa.Must(200, "GET", "/api/v1/crm/nps?businessLine=golf", nil).JSON()
	if nps["overall"].(map[string]any)["responses"].(float64) < 2 || len(nps["comments"].([]any)) == 0 {
		t.Fatalf("NPS: %v", nps)
	}

	// Website complaint form.
	wc := anon(t, inst).Must(201, "POST", "/api/v1/public/complaints", map[string]any{"propertyId": inst.Main, "guest": map[string]any{"name": "Web Visitor " + sfx,
		"email": "web" + sfx + "@tkt.test"}, "subject": "Rude staff", "description": "At the reception desk"}).JSON()
	if wc["number"] == nil || wc["status"] != "open" {
		t.Fatalf("website complaint: %v", wc)
	}

	// Complaint and NPS Reports.
	if rows := engReport(t, sa, "crm.complaint", "params[businessLine]=golf"); len(rows) < 2 {
		t.Fatalf("Complaint Report: %v", rows)
	}
	if rows := engReport(t, sa, "crm.nps", ""); len(rows) < 2 {
		t.Fatalf("NPS Report: %v", rows)
	}
}

// EP-08 acceptance: the spend of the #1 customer of the month equals the
// net folio charges of the month minus refunds. Also: per-line columns,
// segment filter, leaderboards, VIP list (static segment), tier note and
// internal-only access (FR-TOP-06); the engagement KPIs of CRM Performance.
func TestP3EngagementTopSpender(t *testing.T) {
	sa := superAdmin(t, inst)
	sx := roleUser(t, inst, "sales_executive")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	a := customer(t, sa, "TSA"+sfx, "Top Andi "+sfx, nil)
	b := customer(t, sa, "TSB"+sfx, "Top Budi "+sfx, nil)
	pay := func(cust, desc, amount string) string {
		f := engFolio(t, sa, cust, desc, amount)
		return idOf(sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": f, "amount": amount, "methodType": "cash", "channel": "venue"}))
	}
	pay(a, "Wedding tasting "+sfx, "2000000")
	p2 := pay(a, "Golf day "+sfx, "1000000")
	pay(b, "Dinner "+sfx, "1800000")
	sa.Must(201, "POST", "/api/v1/billing/refunds", map[string]any{"paymentId": p2, "amount": "500000", "reason": "Rain"})
	seg := idOf(sa.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "TS" + sfx, "name": "Top test " + sfx, "segmentType": "static"}))
	sa.Must(200, "POST", "/api/v1/crm/segments/"+seg+"/members:add", map[string]any{"customerIds": []string{a, b}})

	r := sx.Must(200, "GET", "/api/v1/crm/top-spenders?period=month&segmentId="+seg, nil).JSON()
	items := r["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("ranking: %v", r)
	}
	first := items[0].(map[string]any)
	if first["customerId"] != a || dec(first["spend"]).IntPart() != 2500000 || dec(first["charges"]).IntPart() != 3000000 || dec(first["refunds"]).IntPart() != 500000 {
		t.Fatalf("AC: #1 spend = net charges − refunds: %v", first)
	}
	found := false
	for _, row := range engReport(t, sa, "crm.top_spender", "") {
		if row["code"] == "TSA"+sfx {
			found = dec(row["spend"]).IntPart() == 2500000
		}
	}
	if !found {
		t.Fatal("Top Spender Report spend of #1 differs from the ranking")
	}
	sx.Must(200, "GET", "/api/v1/crm/top-spenders?period=year&memberType=non_member", nil)
	sx.Must(200, "GET", "/api/v1/crm/leaderboards/rounds?period=month", nil)
	sx.Must(200, "GET", "/api/v1/crm/leaderboards/activity?period=quarter", nil)
	sx.Must(404, "GET", "/api/v1/crm/leaderboards/unknown", nil)

	vip := sx.Must(200, "POST", "/api/v1/crm/top-spenders:add-to-segment", map[string]any{"segmentCode": "VIP" + sfx, "segmentName": "VIP invitation " + sfx,
		"customerIds": []string{a}}).JSON()
	if vip["added"].(float64) != 1 {
		t.Fatalf("VIP list: %v", vip)
	}
	sx.Must(200, "POST", "/api/v1/crm/top-spenders:add-to-segment", map[string]any{"segmentId": vip["segmentId"], "top": 3, "period": "month"})
	acct := idOf(sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": a}))
	sx.Must(201, "POST", "/api/v1/crm/top-spenders:tier-note", map[string]any{"customerId": a, "note": "Consider Gold at next evaluation"})
	if d := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+acct, nil).JSON(); len(d["tierNotes"].([]any)) != 1 {
		t.Fatalf("tier note on the loyalty account: %v", d["tierNotes"])
	}
	roleUser(t, inst, "cashier").Must(403, "GET", "/api/v1/crm/top-spenders", nil)

	// CRM Performance: engagement KPIs after the sales KPIs.
	from := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	to := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	dash := sa.Must(200, "GET", "/api/v1/reporting/dashboards/crm-performance?from="+from+"&to="+to, nil).JSON()
	keys := map[string]bool{}
	for _, k := range dash["kpis"].([]any) {
		keys[str(k.(map[string]any)["key"])] = true
	}
	for _, k := range []string{"leads", "active_customers", "member_activity", "campaign_performance", "loyalty_points_earned", "loyalty_liability",
		"top_spender", "nps", "complaint_sla"} {
		if !keys[k] {
			t.Fatalf("CRM Performance KPI %s missing: %v", k, keys)
		}
	}
}
