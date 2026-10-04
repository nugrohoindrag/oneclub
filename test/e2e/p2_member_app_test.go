package e2e

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/platform/integration"
)

// EP-25: the Member App works only on the signed-in member's own data:
// profile & consent, digital card, membership self-service with online fee
// payment (gateway webhook), vouchers, order food, golf scores privacy.
func TestP2MemberApp(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	mc := roleUser(t, inst, "member")
	if r := mc.Do("GET", "/api/v1/member/profile", nil); r.Status != 403 {
		t.Fatalf("no linked customer: %s", r)
	}
	cust := customer(t, sa, "APP-MEMBER", "Andi Aplikasi", map[string]any{"email": "andi@app.test", "userId": userID(t, "member")})
	sa.Must(201, "POST", "/api/v1/billing/customer-accounts", map[string]any{"customerId": cust, "accountType": "member", "creditLimit": "10000000"})
	prog := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "GOLF-APP", "name": "Golf (app test)", "programKind": "golf"}))
	typ := idOf(sa.Must(201, "POST", "/api/v1/membership/types", map[string]any{"code": "GOLF-APP-IND", "name": "Golf Individual", "programId": prog,
		"annualFee": "1200000", "entitlements": map[string]any{"memberRate": true, "memberCharge": true}}))
	ms := sa.Must(201, "POST", "/api/v1/membership/memberships", map[string]any{"typeId": typ, "customerId": cust, "startDate": dateAgo(0, 8, 0)}).JSON()

	prof := mc.Must(200, "GET", "/api/v1/member/profile", nil).JSON()
	if prof["profile"].(map[string]any)["id"] != cust {
		t.Fatalf("my profile: %v", prof)
	}
	mc.Must(201, "POST", "/api/v1/member/preferences", map[string]any{"category": "allergy", "value": "Shellfish"})
	if p := mc.Must(200, "GET", "/api/v1/member/profile", nil).JSON(); len(p["preferences"].([]any)) != 1 {
		t.Fatalf("own health preference visible to the member: %v", p)
	}
	mc.Must(200, "POST", "/api/v1/member/consent", map[string]any{"profiling": true})
	card := mc.Must(200, "GET", "/api/v1/member/card", nil).JSON()
	if str(card["card"].(map[string]any)["qrToken"]) == "" || len(card["memberships"].([]any)) != 1 {
		t.Fatalf("digital member card: %v", card)
	}
	mine := mc.Must(200, "GET", "/api/v1/member/memberships", nil).Items()
	if len(mine) != 1 || mine[0]["id"] != ms["id"] {
		t.Fatalf("my memberships: %v", mine)
	}
	// Another member's membership is not reachable.
	other := customer(t, sa, "APP-OTHER", "Orang Lain", nil)
	oms := sa.Must(201, "POST", "/api/v1/membership/memberships", map[string]any{"typeId": typ, "customerId": other}).JSON()
	if r := mc.Do("POST", "/api/v1/member/memberships/"+str(oms["id"])+":renew", nil); r.Status != 404 {
		t.Fatalf("other member's membership: %s", r)
	}
	if r := mc.Do("POST", "/api/v1/member/memberships/"+str(ms["id"])+":pause", map[string]any{"from": time.Now().Format("2006-01-02"),
		"until": time.Now().AddDate(0, 2, 0).Format("2006-01-02"), "reason": "Overseas assignment"}); r.Status != 202 {
		t.Fatalf("pause request: %s", r)
	}

	// Online payment of a folio through the gateway; the webhook settles it.
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": map[string]any{"autoPay": false}})
	secret := str(pa.Must(200, "POST", "/api/v1/platform/integrations/"+mp+":rotate-webhook-secret", nil).JSON()["webhookSecret"])
	folio := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"folioType": "walk_in", "businessLine": "golf", "customerId": cust}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+folio+"/charges", map[string]any{"businessLine": "golf", "revenueComponent": "golf_other",
		"description": "Golf lesson", "amount": "350000"}, "Idempotency-Key", newKey())
	op := mc.Must(201, "POST", "/api/v1/member/folios/"+folio+":pay-online", map[string]any{"method": "qris"}).JSON()
	if op["status"] != "pending" || op["qrString"] == nil || op["amount"] != "350000" {
		t.Fatalf("checkout: %v", op)
	}
	body, _ := json.Marshal(map[string]any{"id": "evt_" + uuid.NewString(), "type": "payment.paid", "data": map[string]any{"externalId": op["externalId"], "status": "paid"}})
	hook := anon(t, inst)
	hook.Property = uuid.Nil
	hook.Must(200, "POST", "/api/v1/webhooks/mock-payment", body, integration.SignatureHeader, integration.Sign(secret, body, time.Now()))
	if _, err := inst.App.Dispatcher.DispatchPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A background dispatcher may hold the event; wait for whichever settles it.
	waitFor(t, 10*time.Second, "webhook settles the checkout", func() bool {
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		return mc.Must(200, "GET", "/api/v1/member/online-payments/"+str(op["id"]), nil).JSON()["status"] == "paid"
	})
	if fd := mc.Must(200, "GET", "/api/v1/member/folios/"+folio, nil).JSON(); fd["folio"].(map[string]any)["balance"] != "0" {
		t.Fatalf("folio paid: %v", fd["folio"])
	}
	if r := mc.Do("GET", "/api/v1/member/folios/"+idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"folioType": "walk_in", "customerId": other})), nil); r.Status != 404 {
		t.Fatalf("someone else's folio: %s", r)
	}

	// Vouchers: buy with member charge, see balance and history.
	vt := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "APP-BALL", "name": "Bola 1.000 (app)", "kind": "quota",
		"category": "driving_range_balls", "unit": "ball", "faceValue": "1000", "price": "950000", "validityMonths": 6, "prepaid": true}))
	mc.Must(201, "POST", "/api/v1/member/vouchers:buy", map[string]any{"voucherTypeId": vt, "memberCharge": true}, "Idempotency-Key", newKey())
	if pb := mc.Must(200, "GET", "/api/v1/member/prepaid-balances", nil).Items(); len(pb) != 1 || pb[0]["remaining"] != "1000" {
		t.Fatalf("my prepaid balance: %v", pb)
	}
	if h := mc.Must(200, "GET", "/api/v1/member/voucher-history", nil).Items(); len(h) == 0 {
		t.Fatal("voucher history")
	}
	// Order food (pre-order) charged to the member account.
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "APP-CAFE", "name": "Clubhouse Cafe", "outletType": "restaurant"}))
	prod := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "APP-KOPI", "name": "Kopi Susu", "productType": "beverage", "price": "35000"}))
	o := mc.Must(201, "POST", "/api/v1/member/orders", map[string]any{"outletId": outlet, "lines": []map[string]any{{"productId": prod, "quantity": "2"}},
		"memberCharge": true}, "Idempotency-Key", newKey()).JSON()
	if o["status"] != "paid" || o["source"] != "member_app" {
		t.Fatalf("order food: %v", o)
	}
	if n := len(mc.Must(200, "GET", "/api/v1/member/orders", nil).Items()); n != 1 {
		t.Fatalf("my orders %d", n)
	}
	if st := mc.Must(200, "GET", "/api/v1/member/statements", nil).JSON(); st["totalCharges"] == "0" {
		t.Fatalf("member charges on the statement: %v", st)
	}
	mc.Must(200, "GET", "/api/v1/member/bookings", nil)
	if g := mc.Must(200, "GET", "/api/v1/member/golf/stats", nil).JSON(); g["rounds"].(float64) != 0 {
		t.Fatalf("golf stats: %v", g)
	}
	// Staff-only routes stay closed for members.
	if r := mc.Do("GET", "/api/v1/crm/customers/"+cust+"/360", nil); r.Status != 403 {
		t.Fatalf("member cannot open staff routes: %s", r)
	}
}
