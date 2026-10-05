package e2e

import (
	"testing"
)

// Product-owner decisions pack (P5): editable discount limits (4b) and
// editable approval thresholds (4d).

// pricingPolicyEntry is the Pricing Policies entry of the Club Policies catalogue.
func pricingPolicyEntry(t *testing.T, c *Client) map[string]any {
	t.Helper()
	for _, e := range c.Must(200, "GET", "/api/v1/platform/club-policies/catalog", nil).Items() {
		if e["code"] == "commercial.pricing" {
			return e
		}
	}
	t.Fatal("commercial.pricing is not in the catalogue")
	return nil
}

// Decision 4b: the manual discount limit per role of the Pricing Policies is
// edited in the Staff App (Commercial → Pricing → Discount Limits) through
// the Club Policies API: a new version (audited, 0–100 validated) changes the
// POS supervisor-override threshold and the quotation discount approval.
func TestP5DecisionDiscountLimits(t *testing.T) {
	sa := superAdmin(t, inst)
	cashier := roleUser(t, inst, "cashier")
	sx := roleUser(t, inst, "sales_executive")
	sfx := invSfx()

	entry := pricingPolicyEntry(t, sa)
	base, _ := entry["inForce"].(map[string]any)
	if base == nil {
		base = entry["default"].(map[string]any)
	}
	limits := map[string]any{}
	if m, ok := base["manualDiscountLimits"].(map[string]any); ok {
		for k, v := range m {
			limits[k] = v
		}
	} else {
		for k, v := range entry["default"].(map[string]any)["manualDiscountLimits"].(map[string]any) {
			limits[k] = v
		}
	}
	if limits["cashier"] != "5" || limits["sales_executive"] != "5" {
		t.Fatalf("default tiers in force: %v", limits)
	}
	save := func(want int, l map[string]any) Resp {
		t.Helper()
		v := map[string]any{}
		for k, x := range base {
			v[k] = x
		}
		v["manualDiscountLimits"] = l
		return sa.Must(want, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Pricing Policies", "code": "commercial.pricing",
			"name": entry["name"], "value": v})
	}
	with := func(role, pct string) map[string]any {
		out := map[string]any{}
		for k, v := range limits {
			out[k] = v
		}
		out[role] = pct
		return out
	}

	// validation: 0–100, role codes
	for _, bad := range []struct{ role, pct string }{{"cashier", "150"}, {"cashier", "-1"}, {"cashier", "abc"}, {"Cashier!", "5"}} {
		r := save(422, with(bad.role, bad.pct)).JSON()
		found := false
		for _, e := range r["errors"].([]any) {
			if e.(map[string]any)["field"] == "value.manualDiscountLimits."+bad.role {
				found = true
			}
		}
		if !found {
			t.Fatalf("validation of %s = %s: %v", bad.role, bad.pct, r)
		}
	}

	// before: a cashier gives at most 5% at the POS; a 7% quotation discount needs approval
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "DL" + sfx, "name": "Limit Cafe " + sfx, "outletType": "restaurant"}))
	p := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "DLP" + sfx, "name": "Iced Tea " + sfx, "productType": "food",
		"price": "100000", "outletIds": []string{outlet}}))
	order := func() string {
		o := cashier.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "lines": []map[string]any{{"productId": p}}}).JSON()
		return "/api/v1/commercial/orders/" + str(o["id"]) + "/lines/" + str(o["lines"].([]any)[0].(map[string]any)["id"]) + ":discount"
	}
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "DL" + sfx, "name": "Limit Buyer " + sfx,
		"email": "limit" + sfx + "@buyer.test"}))
	quote := func() map[string]any {
		return sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "title": "Limit " + sfx, "pricingMode": "nett",
			"discountPercent": "7", "lines": []map[string]any{{"itemType": "venue", "description": "Ballroom", "quantity": "1", "unitPrice": "10000000"}}},
			"Idempotency-Key", newKey()).JSON()
	}
	cashier.Must(403, "POST", order(), map[string]any{"percent": "7", "reason": "Regular"})
	if q := quote(); q["approvalStatus"] != "required" {
		t.Fatalf("7%% above the 5%% limit of a sales executive needs approval: %v", q["approvalStatus"])
	}

	// the screen saves a new version: cashier and sales executive up to 10%
	l := with("cashier", "10")
	l["sales_executive"] = "10"
	v := save(201, l).JSON()
	t.Cleanup(func() {
		sa.Must(200, "POST", "/api/v1/platform/club-policies/"+str(v["id"])+":set-status", map[string]any{"status": "inactive", "reason": "test cleanup"})
	})
	if v["version"].(float64) < 1 || v["code"] != "commercial.pricing" {
		t.Fatalf("new version: %v", v)
	}
	if e := pricingPolicyEntry(t, sa); e["version"] != v["version"] ||
		e["inForce"].(map[string]any)["manualDiscountLimits"].(map[string]any)["cashier"] != "10" {
		t.Fatalf("version in force: %v", e)
	}
	var audited int
	sysQueryRow(t, inst, `SELECT count(*) FROM audit.audit_log WHERE entity_type = 'platform.club_policy' AND entity_id = $1`, []any{v["id"]}, &audited)
	if audited != 1 {
		t.Fatalf("the new version is audited: %d", audited)
	}

	// after: the POS and the quotation follow the new limits
	cashier.Must(200, "POST", order(), map[string]any{"percent": "7", "reason": "Regular"})
	cashier.Must(403, "POST", order(), map[string]any{"percent": "11", "reason": "Regular"})
	if q := quote(); q["approvalStatus"] != "not_required" {
		t.Fatalf("7%% within the new 10%% limit: %v", q["approvalStatus"])
	}
}

// Decision 4d: a Property Admin edits the amount thresholds of the approval
// workflow of a document type (here the purchase order) in Settings →
// Approval Workflows: create, validate, edit the threshold, reorder and
// deactivate the steps (audited); a purchase order below / above the
// threshold routes accordingly. Workflows for all properties need an
// instance-wide administrator.
func TestP5DecisionApprovalThresholds(t *testing.T) {
	sa := superAdmin(t, inst)
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword) // MAIN only
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "AT", nil)
	item := k.item(t, sa, "ATI", k.pcs, nil)
	sup := prcSupplier(t, sa, "ATS"+k.sfx, nil)
	pmRole, finRole, gmRole := roleID(t, sa, "procurement_manager"), roleID(t, sa, "finance_manager"), roleID(t, sa, "general_manager")
	step := func(no int, name, role string, above any) map[string]any {
		conds := []map[string]any{}
		if above != nil {
			conds = append(conds, map[string]any{"attribute": "amount", "operator": "gt", "value": above})
		}
		return map[string]any{"stepNo": no, "name": name, "approverType": "role", "approverRoleId": role, "conditions": conds, "slaHours": 24}
	}

	// the instance-wide demo workflow needs an instance-wide administrator
	demo := sa.Must(200, "GET", "/api/v1/platform/approval-workflows?filter[documentType]=purchase_order", nil).Items()
	for _, w := range demo {
		if w["propertyId"] == nil {
			if r := pa.Do("PATCH", "/api/v1/platform/approval-workflows/"+str(w["id"]), map[string]any{"priority": 50}); r.Status != 403 {
				t.Fatalf("property admin edits a workflow of all properties: %s", r)
			}
		}
	}
	pa.Must(403, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "purchase_order", "name": "All properties " + k.sfx,
		"steps": []map[string]any{step(1, "Procurement Manager", pmRole, nil)}})
	pa.Must(403, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "purchase_order", "name": "Other property " + k.sfx,
		"propertyId": inst.MDR, "steps": []map[string]any{step(1, "Procurement Manager", pmRole, nil)}})

	// validation of the thresholds
	for _, bad := range []any{-5, "five million", ""} {
		r := pa.Must(422, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "purchase_order", "name": "Bad " + k.sfx,
			"propertyId": inst.Main, "steps": []map[string]any{step(1, "Finance", finRole, bad)}}).JSON()
		if e := r["errors"].([]any)[0].(map[string]any); e["field"] != "steps[0].conditions[0].value" {
			t.Fatalf("threshold %v: %v", bad, r)
		}
	}
	pa.Must(422, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "purchase_order", "name": "Bad " + k.sfx,
		"propertyId": inst.Main, "priority": -1, "steps": []map[string]any{step(1, "Finance", finRole, 1)}})

	// the Property Admin creates the matrix of the property: Finance above Rp7 jt, GM above Rp25 jt
	wf := pa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "purchase_order", "name": "PO matrix MAIN " + k.sfx,
		"propertyId": inst.Main, "priority": 1, "steps": []map[string]any{step(1, "Procurement Manager", pmRole, nil),
			step(2, "Finance Manager", finRole, 7000000), step(3, "General Manager", gmRole, 25000000)}}).JSON()
	wid := str(wf["id"])
	t.Cleanup(func() {
		sa.Do("PATCH", "/api/v1/platform/approval-workflows/"+wid, map[string]any{"status": "inactive"})
	})

	// a purchase order of Rp6.66 jt (6 jt + PPN 11%)
	route := func() []string {
		t.Helper()
		po := sa.Must(201, "POST", "/api/v1/procurement/purchase-orders", map[string]any{"supplierId": sup, "warehouseId": wh, "submit": true,
			"lines": []map[string]any{{"itemId": item, "quantity": "6", "unitPrice": "1000000"}}}, "Idempotency-Key", newKey()).JSON()
		prcStatus(t, po, "pending_approval")
		ar := sa.Must(200, "GET", "/api/v1/platform/approvals/"+str(po["approvalRequestId"]), nil).JSON()
		var out []string
		for _, s := range ar["steps"].([]any) {
			m := s.(map[string]any)
			out = append(out, str(m["name"])+":"+str(m["status"]))
		}
		return out
	}
	want := func(got []string, w ...string) {
		t.Helper()
		if len(got) != len(w) {
			t.Fatalf("routing %v, want %v", got, w)
		}
		for i := range w {
			if got[i] != w[i] {
				t.Fatalf("routing %v, want %v", got, w)
			}
		}
	}
	want(route(), "Procurement Manager:pending", "Finance Manager:skipped", "General Manager:skipped")

	// the threshold is lowered to Rp5 jt: the same amount now goes to Finance
	pa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+wid, map[string]any{"steps": []map[string]any{step(1, "Procurement Manager", pmRole, nil),
		step(2, "Finance Manager", finRole, 5000000), step(3, "General Manager", gmRole, 25000000)}})
	want(route(), "Procurement Manager:pending", "Finance Manager:waiting", "General Manager:skipped")

	// reordered: Finance first
	got := pa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+wid, map[string]any{"steps": []map[string]any{
		step(1, "Finance Manager", finRole, 5000000), step(2, "Procurement Manager", pmRole, nil), step(3, "General Manager", gmRole, 25000000)}}).JSON()
	if s := got["steps"].([]any); s[0].(map[string]any)["name"] != "Finance Manager" || s[1].(map[string]any)["name"] != "Procurement Manager" {
		t.Fatalf("reordered steps: %v", s)
	}
	want(route(), "Finance Manager:pending", "Procurement Manager:waiting", "General Manager:skipped")

	// deactivated: the workflow for all properties applies again
	pa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+wid, map[string]any{"status": "inactive"})
	for _, s := range route() {
		if s == "Finance Manager:pending" || s == "Procurement Manager:waiting" {
			t.Fatalf("deactivated workflow still routes: %s", s)
		}
	}
	var audited int
	sysQueryRow(t, inst, `SELECT count(*) FROM audit.audit_log WHERE entity_type = 'platform.approval_workflow' AND entity_id = $1 AND property_id = $2`,
		[]any{wid, inst.Main}, &audited)
	if audited != 4 {
		t.Fatalf("create, two edits and the deactivation are audited: %d", audited)
	}
}
