package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// resourceCRUDModules and resourceCRUDSkip let later areas (P3/P4) add their
// modules to the generic CRUD test, or skip a resource covered by their own
// tests, from an init() in their own test file.
var (
	resourceCRUDModules = map[string]bool{}
	resourceCRUDSkip    = map[string]string{}
)

// Every master data resource of P2 can be created, edited and (when unused)
// deleted through the generic engine — driven by the resource definitions
// the Back Office renders its screens from (GET /platform/resource-definitions).
func TestP2ResourceDefinitionsCRUD(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	defs := sa.Must(200, "GET", "/api/v1/platform/resource-definitions", nil).Items()
	modules := map[string]bool{"golf": true, "sportclub": true, "stay": true, "commercial": true, "inventory": true, "membership": true, "crm": true,
		"reservation": true, "platform": true}
	skip := map[string]string{
		"crm.customer": "covered by P0", "crm.guest": "covered by P0", "platform.venue": "P0", "platform.property": "P0", "platform.department": "P0",
		"platform.employee": "P0", "procurement.supplier": "P0", "billing.payment_method": "P0", "commercial.tax_service": "P0", "golf.course": "P0",
		"commercial.outlet": "P0", "commercial.product": "P0", "reservation.resource": "P0", "membership.member": "P0",
		"commercial.pricing_rule": "versioned rules are covered by the pricing tests",
	}
	for k, v := range resourceCRUDModules {
		modules[k] = v
	}
	for k, v := range resourceCRUDSkip {
		skip[k] = v
	}
	// Resources of P1 (commit a48e6a3) are covered by the P1 tests.
	for _, k := range []string{"commercial.day_type", "commercial.rate_plan", "commercial.time_band", "crm.corporate_account", "crm.corporate_nominee",
		"crm.customer_preference", "crm.customer_relationship", "golf.caddy", "golf.course_asset", "golf.course_section", "golf.golf_cart", "golf.hole",
		"golf.locker", "golf.playing_route", "golf.tee_set", "golf.tee_sheet_template", "membership.package", "membership.program", "membership.type",
		"platform.calendar_day", "sportclub.facility"} {
		skip[k] = "P1"
	}
	type made struct {
		key, path, id, edit string
		editValue           any
		body                map[string]any
		noDelete            bool
	}
	var todo []map[string]any
	for _, d := range defs {
		if modules[str(d["module"])] && skip[str(d["key"])] == "" {
			todo = append(todo, d)
		}
	}
	n := len(todo)
	// Seed a facility, an outlet, a product and two UOMs so dependent resources have references.
	sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": fmt.Sprintf("RXF%d", time.Now().Unix()%100000), "name": "RX Facility", "facilityType": "tennis", "capacity": 4})
	sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": fmt.Sprintf("RXO%d", time.Now().Unix()%100000), "name": "RX Outlet", "outletType": "restaurant"})
	sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": fmt.Sprintf("RXP%d", time.Now().Unix()%100000), "name": "RX Product", "productType": "food", "price": "1000"})
	for i := 0; i < 2; i++ {
		sa.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": fmt.Sprintf("RXU%d%d", time.Now().Unix()%100000, i), "name": "RX UOM"})
	}
	var created []made
	last := map[string]string{}
	// Create in passes: a resource whose reference list is still empty waits
	// for the next pass.
	for pass := 0; pass < 5 && len(todo) > 0; pass++ {
		var next []map[string]any
		for _, d := range todo {
			key, path := str(d["key"]), str(d["path"])
			body := map[string]any{}
			var edit string
			var editValue any
			missing := false
			for _, fl := range d["fields"].([]any) {
				fm := fl.(map[string]any)
				name, typ := str(fm["name"]), str(fm["type"])
				if fm["readOnly"] == true {
					continue
				}
				if fm["required"] == true || name == "code" || name == "name" || name == "startTime" || name == "endTime" || (key == "inventory.recipe_line" && name == "itemId") {
					v, ok := synth(t, sa, key, name, typ, fm)
					if !ok && fm["required"] == true {
						missing = true
					}
					if ok {
						body[name] = v
					}
				}
				if edit == "" && fm["createOnly"] != true && name != "code" && name != "status" {
					switch typ {
					case "text", "textarea":
						edit, editValue = name, "Edited "+key
					case "number":
						edit, editValue = name, 2
					case "decimal":
						edit, editValue = name, "2"
					case "boolean":
						edit, editValue = name, true
					}
				}
			}
			if missing {
				next = append(next, d)
				last[key] = "a referenced list is empty"
				continue
			}
			r := sa.Do("POST", path, body, "Idempotency-Key", newKey())
			if r.Status != 201 {
				next = append(next, d)
				last[key] = fmt.Sprintf("create %d %s (body %v)", r.Status, r.Body, body)
				continue
			}
			created = append(created, made{key: key, path: path, id: str(r.JSON()["id"]), edit: edit, editValue: editValue, body: body, noDelete: d["noDelete"] == true})
		}
		todo = next
	}
	var problems []string
	for _, d := range todo {
		problems = append(problems, str(d["key"])+" "+last[str(d["key"])])
	}
	for _, m := range created {
		patch := map[string]any{"status": "inactive"}
		if m.edit != "" {
			patch = map[string]any{m.edit: m.editValue}
		}
		if p := sa.Do("PATCH", m.path+"/"+m.id, patch); p.Status != 200 {
			problems = append(problems, fmt.Sprintf("%s patch %d %s", m.key, p.Status, p.Body))
		}
	}
	for i := len(created) - 1; i >= 0; i-- {
		m := created[i]
		if m.noDelete {
			continue
		}
		del := sa.Do("DELETE", m.path+"/"+m.id, nil)
		if del.Status == 409 {
			// Archived children keep the parent in use: delete a fresh, unused copy.
			fresh := map[string]any{}
			for k, v := range m.body {
				fresh[k] = v
			}
			if _, ok := fresh["code"]; ok {
				fresh["code"] = fmt.Sprintf("RY%d%03d", time.Now().Unix()%100000, i)
			}
			if r := sa.Do("POST", m.path, fresh, "Idempotency-Key", newKey()); r.Status == 201 {
				del = sa.Do("DELETE", m.path+"/"+str(r.JSON()["id"]), nil)
			}
		}
		if del.Status != 204 {
			problems = append(problems, fmt.Sprintf("%s delete %d %s", m.key, del.Status, del.Body))
		}
	}
	if n < 33 {
		t.Fatalf("only %d P2 resource definitions found", n)
	}
	if len(problems) > 0 {
		t.Fatalf("%d resource problems:\n%s", len(problems), strings.Join(problems, "\n"))
	}
}

var synthSeq int

// synth makes a valid value for a field of a resource.
func synth(t *testing.T, c *Client, key, name, typ string, fm map[string]any) (any, bool) {
	synthSeq++
	opts, _ := fm["options"].([]any)
	switch {
	case name == "code" && key == "reservation.resource_type":
		return fmt.Sprintf("rx_type_%d", synthSeq), true
	case name == "code":
		return fmt.Sprintf("RX%d%03d", time.Now().Unix()%100000, synthSeq), true
	case name == "startTime":
		return "08:00", true
	case name == "endTime":
		return "17:00", true
	case name == "name" || name == "title":
		return "Resource " + key, true
	case typ == "select" && len(opts) > 0:
		return pickOption(key, name, opts), true
	case typ == "reference":
		ref, _ := fm["refPath"].(string)
		if ref == "" {
			return nil, false
		}
		items := c.Must(200, "GET", ref+"?limit=5", nil).Items()
		if len(items) == 0 {
			return nil, false
		}
		if strings.HasPrefix(name, "to") && len(items) > 1 {
			return items[1]["id"], true
		}
		return items[0]["id"], true
	case typ == "number":
		return 1, true
	case typ == "decimal":
		return "1", true
	case typ == "date":
		return time.Now().AddDate(0, 0, 400+synthSeq).Format("2006-01-02"), true
	case typ == "time":
		return "08:00", true
	case typ == "datetime":
		return time.Now().Add(time.Hour).Format(time.RFC3339), true
	case typ == "boolean":
		return false, true
	case typ == "list":
		return []string{"X"}, true
	case typ == "intlist":
		return []int{1}, true
	case typ == "json":
		return map[string]any{}, true
	case typ == "jsonlist":
		return []any{}, true
	case typ == "email":
		return "rx@resource.test", true
	}
	return "Value", true
}

func pickOption(key, name string, opts []any) any {
	// Prefer options that need no further configuration.
	for _, want := range []string{"other", "active", "individual", "golf", "outdoor", "pre_op", "exclusive", "time_slot", "sportclub"} {
		for _, o := range opts {
			if str(o) == want {
				return o
			}
		}
	}
	_ = key
	_ = name
	return opts[0]
}
