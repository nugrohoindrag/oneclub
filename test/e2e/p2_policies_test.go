package e2e

import "testing"

// EP-28: the Club Policies catalogue lists every P2 policy with its NC §25
// category and default; a new version is validated against the policy type
// and takes effect for the module (Voucher Policies: extension limit).
func TestP2ClubPolicies(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	cat := sa.Must(200, "GET", "/api/v1/platform/club-policies/catalog", nil).Items()
	cats := map[string]bool{}
	for _, e := range cat {
		cats[str(e["category"])] = true
	}
	for _, c := range []string{"Sport Club Policies", "Stay Policies", "Member Policies", "Caddy Policies", "Golf Cart Policies", "Voucher Policies",
		"POS Policies", "Reciprocal Policies", "Hall of Fame Policies", "Cancellation Policies"} {
		if !cats[c] {
			t.Fatalf("catalogue misses %s: %v", c, cats)
		}
	}
	if r := sa.Do("POST", "/api/v1/platform/club-policies", map[string]any{"category": "Voucher Policies", "code": "commercial.voucher", "name": "Voucher",
		"value": map[string]any{"maxExtensionMonths": "six"}}); r.Status != 422 {
		t.Fatalf("typed validation: %s", r)
	}
	if r := sa.Do("POST", "/api/v1/platform/club-policies", map[string]any{"category": "POS Policies", "code": "commercial.voucher", "name": "Voucher",
		"value": map[string]any{"maxExtensionMonths": 1}}); r.Status != 422 {
		t.Fatalf("category must match the catalogue: %s", r)
	}
	sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Voucher Policies", "code": "commercial.voucher", "name": "Voucher",
		"value": map[string]any{"maxExtensionMonths": 1, "reminderDays": []int{30, 7}, "transferAllowed": false}})
	t.Cleanup(func() {
		sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Voucher Policies", "code": "commercial.voucher", "name": "Voucher",
			"value": map[string]any{"maxExtensionMonths": 24, "reminderDays": []int{30, 7}, "transferAllowed": true}})
	})
	for _, e := range sa.Must(200, "GET", "/api/v1/platform/club-policies/catalog", nil).Items() {
		if e["code"] == "commercial.voucher" && e["version"].(float64) != 1 {
			t.Fatalf("version in force: %v", e)
		}
	}
	vt := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "POL-5X", "name": "Policy test 5x", "kind": "quota",
		"category": "sport_entry", "unit": "entry", "faceValue": "5", "price": "500000", "validityMonths": 3, "transferable": true}))
	v := sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": vt, "customerId": f.CustomerA,
		"payment": map[string]any{"methodType": "cash"}}, "Idempotency-Key", newKey()).JSON()["vouchers"].([]any)[0].(map[string]any)
	if r := sa.Do("POST", "/api/v1/commercial/vouchers/"+str(v["id"])+":transfer", map[string]any{"customerId": f.CustomerB, "reason": "gift"}); r.Status != 409 {
		t.Fatalf("transfer disabled by policy: %s", r)
	}
	if r := sa.Do("POST", "/api/v1/commercial/vouchers/"+str(v["id"])+":extend", map[string]any{"expiresAt": dateAgo(-1, 0, 0) + "T00:00:00Z", "reason": "x"}); r.Status != 422 {
		t.Fatalf("extension beyond the policy limit: %s", r)
	}
}
