package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// EP-02 AC: one organization with ≥2 properties, each with ≥2 venues and
// courses; a user of property A sees nothing of property B.
func TestOrganizationStructure(t *testing.T) {
	sa := superAdmin(t, inst)
	org := sa.Must(200, "PATCH", "/api/v1/platform/organization", map[string]any{"legalName": "PT Modern Golf Indonesia",
		"npwp": "01.234.567.8-901.000", "city": "Tangerang", "email": "info@moderngolf.test"}).JSON()
	if org["legalName"] != "PT Modern Golf Indonesia" {
		t.Fatalf("organization: %v", org)
	}
	sa.Must(422, "PATCH", "/api/v1/platform/organization", map[string]any{"npwp": "123"})
	sa.Must(200, "GET", "/api/v1/platform/organization", nil)
	sa.Must(422, "POST", "/api/v1/platform/properties", map[string]any{"code": "bad code", "name": "x"})
	sa.Must(422, "POST", "/api/v1/platform/properties", map[string]any{"code": "TZX", "name": "x", "timezone": "Nowhere/City"})
	p := sa.Must(201, "POST", "/api/v1/platform/properties", map[string]any{"code": "bsd", "name": "BSD Club", "timezone": "Asia/Jakarta"}).JSON()
	if p["code"] != "BSD" {
		t.Fatalf("code must be upper-cased: %v", p["code"])
	}
	sa.Must(200, "PATCH", "/api/v1/platform/properties/"+str(p["id"]), map[string]any{"city": "Tangerang Selatan"})
	sa.Must(200, "GET", "/api/v1/platform/properties/"+str(p["id"]), nil)

	// counts per property
	for _, prop := range []string{inst.Main.String(), inst.MDR.String()} {
		c := superAdmin(t, inst)
		c.Property = mustUUID(prop)
		if n := len(c.Must(200, "GET", "/api/v1/platform/venues", nil).Items()); n < 2 {
			t.Fatalf("property %s has %d venues", prop, n)
		}
		// the demo club's championship course (the driving range has none)
		if n := len(c.Must(200, "GET", "/api/v1/golf/courses", nil).Items()); prop == inst.Main.String() && n < 1 {
			t.Fatalf("property %s has %d courses", prop, n)
		}
	}
	// Property admin of MDR does not see MAIN venues (list, view, API).
	pa2 := login(t, inst, "property.admin2@demo.oneclub.id", demoPassword)
	pa2.Property = inst.MDR
	for _, v := range pa2.Must(200, "GET", "/api/v1/platform/venues", nil).Items() {
		if v["propertyId"] != inst.MDR.String() {
			t.Fatalf("foreign venue visible: %v", v)
		}
	}
	mainVenue := superAdmin(t, inst).Must(200, "GET", "/api/v1/platform/venues", nil).Items()[0]
	pa2.Must(404, "GET", "/api/v1/platform/venues/"+str(mainVenue["id"]), nil)
	pa2.Must(404, "PATCH", "/api/v1/platform/venues/"+str(mainVenue["id"]), map[string]any{"name": "Hijack"})
	pa2.Must(403, "POST", "/api/v1/platform/properties", map[string]any{"code": "NOPE", "name": "Nope"})
}

// FR-ORG-03/04/05, FR-MD-01/02: venues, courses, departments, employees;
// used entities cannot be deleted, only set Inactive.
func TestVenueCourseDepartmentEmployee(t *testing.T) {
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	// Venue Activation flag is on: new venues start Pending.
	v := pa.Must(201, "POST", "/api/v1/platform/venues", map[string]any{"code": "RANGE", "name": "Driving Range", "venueType": "golf"}).JSON()
	if v["status"] != "pending" {
		t.Fatalf("new venue should be pending activation: %v", v)
	}
	pa.Must(409, "PATCH", "/api/v1/platform/venues/"+str(v["id"]), map[string]any{"status": "active"})
	pa.Must(422, "POST", "/api/v1/platform/venues", map[string]any{"code": "RANGE", "name": "Duplicate"})
	// A course needs a venue of the same property.
	var mdrVenue string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.venues WHERE property_id = $1 LIMIT 1`, []any{inst.MDR}, &mdrVenue)
	pa.Must(422, "POST", "/api/v1/golf/courses", map[string]any{"code": "X", "name": "X", "venueId": mdrVenue, "holes": 18})
	pa.Must(422, "POST", "/api/v1/golf/courses", map[string]any{"code": "X", "name": "X", "venueId": v["id"], "holes": 17})
	c := pa.Must(201, "POST", "/api/v1/golf/courses", map[string]any{"code": "PAR3", "name": "Par 3 Course", "venueId": v["id"], "holes": 9}).JSON()
	pa.Must(200, "PATCH", "/api/v1/golf/courses/"+str(c["id"]), map[string]any{"name": "Par-3 Course"})
	pa.Must(200, "GET", "/api/v1/golf/courses/"+str(c["id"]), nil)
	// The venue is used by the course: delete is refused, Inactive allowed.
	pa.Must(409, "DELETE", "/api/v1/platform/venues/"+str(v["id"]), nil)
	pa.Must(204, "DELETE", "/api/v1/golf/courses/"+str(c["id"]), nil) // unused course is archived
	pa.Must(404, "GET", "/api/v1/golf/courses/"+str(c["id"])+"x", nil)
	for _, x := range pa.Must(200, "GET", "/api/v1/golf/courses", nil).Items() {
		if x["id"] == c["id"] {
			t.Fatal("archived course still listed")
		}
	}
	if !strings.Contains(string(pa.Must(200, "GET", "/api/v1/golf/courses?includeArchived=true", nil).Body), str(c["id"])) {
		t.Fatal("archived course missing with includeArchived")
	}

	// Departments with hierarchy and no cycles.
	parent := pa.Must(201, "POST", "/api/v1/platform/departments", map[string]any{"code": "OPS", "name": "Operations"}).JSON()
	child := pa.Must(201, "POST", "/api/v1/platform/departments", map[string]any{"code": "OPS-GOLF", "name": "Golf Ops", "parentId": parent["id"]}).JSON()
	pa.Must(422, "PATCH", "/api/v1/platform/departments/"+str(parent["id"]), map[string]any{"parentId": child["id"]})
	pa.Must(200, "GET", "/api/v1/platform/departments?filter[parentId]="+str(parent["id"]), nil)
	// Employees linked to a department and a user.
	e := pa.Must(201, "POST", "/api/v1/platform/employees", map[string]any{"employeeNo": "emp-001", "fullName": "Andi Caddy Master",
		"departmentId": child["id"], "jobTitle": "Caddy Master", "joinDate": "2024-01-15"}).JSON()
	if e["employeeNo"] != "EMP-001" || e["joinDate"] != "2024-01-15" {
		t.Fatalf("employee: %v", e)
	}
	pa.Must(422, "POST", "/api/v1/platform/employees", map[string]any{"employeeNo": "EMP-002", "fullName": "Bad Date", "joinDate": "2024-13-45"})
	pa.Must(409, "DELETE", "/api/v1/platform/departments/"+str(child["id"]), nil) // used by employee
	pa.Must(200, "PATCH", "/api/v1/platform/departments/"+str(child["id"]), map[string]any{"status": "inactive"})
	pa.Must(200, "PATCH", "/api/v1/platform/employees/"+str(e["id"]), map[string]any{"jobTitle": "Senior Caddy Master"})
	pa.Must(200, "GET", "/api/v1/platform/employees/"+str(e["id"]), nil)
	pa.Must(204, "DELETE", "/api/v1/platform/employees/"+str(e["id"]), nil)
	sa := superAdmin(t, inst)
	tmp := sa.Must(201, "POST", "/api/v1/platform/properties", map[string]any{"code": "TEMP", "name": "Temporary"}).JSON()
	sa.Must(204, "DELETE", "/api/v1/platform/properties/"+str(tmp["id"]), nil)
	sa.Must(409, "DELETE", "/api/v1/platform/properties/"+inst.MDR.String(), nil) // has venues
	pa.Must(200, "GET", "/api/v1/platform/departments/"+str(parent["id"]), nil)
	pa.Must(200, "GET", "/api/v1/platform/venues/"+str(v["id"]), nil)
	_ = time.Now
}

// EP-04 AC: importing 1,000 Customer rows with 10 invalid rows inserts 990
// and reports 10 row errors; re-running is idempotent (updates, no duplicates).
func TestImportThousandCustomers(t *testing.T) {
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	var b strings.Builder
	b.WriteString("code,name,customerType,email,phone\n")
	for i := 1; i <= 1000; i++ {
		if i%100 == 0 { // 10 invalid rows: bad e-mail
			fmt.Fprintf(&b, "C%04d,Customer %d,individual,not-an-email,0812\n", i, i)
		} else {
			fmt.Fprintf(&b, "C%04d,Customer %d,individual,c%d@example.test,0812%06d\n", i, i, i, i)
		}
	}
	csv := b.String()
	prev := pa.Must(200, "POST", "/api/v1/platform/imports", map[string]any{"entity": "crm.customer", "mode": "preview", "csv": csv}).JSON()
	if prev["insertedRows"].(float64) != 990 || prev["failedRows"].(float64) != 10 {
		t.Fatalf("preview: %v", prev)
	}
	if n := len(pa.Must(200, "GET", "/api/v1/crm/customers?limit=5&q=c1%40example.test", nil).Items()); n != 0 {
		t.Fatalf("preview must not save rows, found %d", n)
	}
	res := pa.Must(201, "POST", "/api/v1/platform/imports", map[string]any{"entity": "crm.customer", "mode": "commit", "csv": csv, "filename": "rhapsody-customers.csv"}).JSON()
	if res["totalRows"].(float64) != 1000 || res["insertedRows"].(float64) != 990 || res["failedRows"].(float64) != 10 {
		t.Fatalf("import result: %v", res)
	}
	errsList := res["errors"].([]any)
	if len(errsList) != 10 {
		t.Fatalf("expected 10 row errors, got %d", len(errsList))
	}
	first := errsList[0].(map[string]any)
	if first["row"].(float64) != 101 || first["field"] != "email" {
		t.Fatalf("row error should point to line 101 / email: %v", first)
	}
	again := pa.Must(201, "POST", "/api/v1/platform/imports", map[string]any{"entity": "crm.customer", "csv": csv}).JSON()
	if again["insertedRows"].(float64) != 0 || again["updatedRows"].(float64) != 990 {
		t.Fatalf("re-import must be idempotent: %v", again)
	}
	var count int
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.customers WHERE property_id = $1 AND code LIKE 'C____'`, []any{inst.Main}, &count)
	if count != 990 {
		t.Fatalf("expected 990 customers, got %d", count)
	}
	pa.Must(200, "GET", "/api/v1/platform/imports", nil)
	ents := pa.Must(200, "GET", "/api/v1/platform/imports/entities", nil).Items()
	if len(ents) < 10 {
		t.Fatalf("importable entities: %d", len(ents))
	}
	// Unknown column is rejected up-front.
	bad := pa.Must(201, "POST", "/api/v1/platform/imports", map[string]any{"entity": "crm.customer", "csv": "code,favouriteColour\nX1,blue\n"}).JSON()
	if bad["status"] != "failed" {
		t.Fatalf("unknown column must fail: %v", bad)
	}
	// Multipart upload works too.
	body, ctype := multipartBody(t, map[string]string{"entity": "crm.guest", "mode": "commit"}, "file", "guests.csv", "code,name\nG1,Guest One\nG2,Guest Two\n")
	r := pa.Do("POST", "/api/v1/platform/imports", body, "Content-Type", ctype)
	if r.Status != 201 || r.JSON()["insertedRows"].(float64) != 2 {
		t.Fatalf("multipart import: %s", r)
	}
}

// FR-MD-07: export CSV and XLSX, honouring filters.
func TestExport(t *testing.T) {
	sa := superAdmin(t, inst)
	r := sa.Must(200, "GET", "/api/v1/platform/venues:export?format=csv&filter[venueType]=golf", nil)
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") || !strings.Contains(string(r.Body), "code,name,venueType") {
		t.Fatalf("csv export: %s", r.Header)
	}
	x := sa.Must(200, "GET", "/api/v1/platform/venues:export?format=xlsx", nil)
	if !strings.Contains(x.Header.Get("Content-Type"), "spreadsheetml") || len(x.Body) < 100 || string(x.Body[:2]) != "PK" {
		t.Fatal("xlsx export invalid")
	}
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	gm.Must(403, "GET", "/api/v1/platform/venues:export", nil)
}

// FR-MD-04 / FR-MD-08 / EP-04 AC: a future-dated Tax & Service change does
// not affect calculations before its effective date.
func TestTaxServiceEffectiveDate(t *testing.T) {
	// MDR has no golf rate card (MAIN carries the seeded PPN of the demo).
	pa := login(t, inst, "property.admin2@demo.oneclub.id", demoPassword)
	pa.Property = inst.MDR
	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	pa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": "SVC", "name": "Service Charge", "kind": "service",
		"ratePercent": "10", "basis": "net_amount", "pricingMode": "plus_plus", "sequence": 1, "effectiveFrom": past})
	pb1 := pa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": "PB1", "name": "PB1", "kind": "tax",
		"ratePercent": "10", "basis": "net_plus_service", "pricingMode": "plus_plus", "sequence": 2, "effectiveFrom": past}).JSON()
	// Already-effective versions are immutable (except deactivation) and
	// cannot be back-dated.
	pa.Must(409, "PATCH", "/api/v1/commercial/tax-service-rules/"+str(pb1["id"]), map[string]any{"ratePercent": "11"})
	pa.Must(422, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": "PB1", "name": "PB1", "kind": "tax",
		"ratePercent": "11", "basis": "net_plus_service", "pricingMode": "plus_plus", "effectiveFrom": past})
	pa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": "PB1", "name": "PB1", "kind": "tax",
		"ratePercent": "12", "basis": "net_plus_service", "pricingMode": "plus_plus", "sequence": 2, "effectiveFrom": future})

	now := pa.Must(200, "POST", "/api/v1/commercial/tax-service-rules:calculate", map[string]any{"amount": "100000"}).JSON()
	// plus-plus: 100,000 + 10% service = 110,000; tax 10% of 110,000 = 11,000; total 121,000
	if now["total"] != "121000" || now["netAmount"] != "100000" {
		t.Fatalf("current calculation: %v", now)
	}
	later := pa.Must(200, "POST", "/api/v1/commercial/tax-service-rules:calculate", map[string]any{"amount": "100000",
		"at": time.Now().Add(31 * 24 * time.Hour).UTC().Format(time.RFC3339)}).JSON()
	if later["total"] != "123200" { // 110,000 * 12% = 13,200
		t.Fatalf("future calculation: %v", later)
	}
	again := pa.Must(200, "POST", "/api/v1/commercial/tax-service-rules:calculate", map[string]any{"amount": "100000"}).JSON()
	if again["total"] != "121000" {
		t.Fatalf("future change leaked into today: %v", again)
	}
	pa.Must(409, "PATCH", "/api/v1/commercial/tax-service-rules/"+str(pb1["id"]), map[string]any{"name": "Pajak Restoran (PB1)"})
	pa.Must(200, "PATCH", "/api/v1/commercial/tax-service-rules/"+str(pb1["id"]), map[string]any{"status": "inactive"})
	list := pa.Must(200, "GET", "/api/v1/commercial/tax-service-rules", nil).Items()
	if len(list) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(list))
	}
	pa.Must(200, "GET", "/api/v1/commercial/tax-service-rules/"+str(pb1["id"]), nil)
	pa.Must(200, "GET", "/api/v1/commercial/tax-service-rules:export", nil)
}

// FR-MD-03 / FR-MD-08: payment methods and effective-dated availability.
func TestPaymentMethods(t *testing.T) {
	sa := superAdmin(t, inst)
	pms := sa.Must(200, "GET", "/api/v1/billing/payment-methods", nil).Items()
	if len(pms) != 8 {
		t.Fatalf("expected the 8 standard payment methods, got %d", len(pms))
	}
	var qris map[string]any
	for _, p := range pms {
		if p["methodType"] == "qris" {
			qris = p
		}
	}
	pm := sa.Must(201, "POST", "/api/v1/billing/payment-methods", map[string]any{"code": "EDC-BCA", "name": "EDC BCA", "methodType": "card"}).JSON()
	sa.Must(200, "PATCH", "/api/v1/billing/payment-methods/"+str(pm["id"]), map[string]any{"sortOrder": 95})
	sa.Must(200, "GET", "/api/v1/billing/payment-methods/"+str(pm["id"]), nil)
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	pa.Must(201, "POST", "/api/v1/billing/payment-method-settings", map[string]any{"paymentMethodId": qris["id"], "enabled": true, "surchargePercent": "0.7"})
	future := time.Now().Add(7 * 24 * time.Hour).UTC()
	pa.Must(201, "POST", "/api/v1/billing/payment-method-settings", map[string]any{"paymentMethodId": qris["id"], "enabled": false,
		"surchargePercent": "0", "effectiveFrom": future.Format(time.RFC3339)})
	pa.Must(422, "POST", "/api/v1/billing/payment-method-settings", map[string]any{"paymentMethodId": qris["id"], "enabled": true,
		"surchargePercent": "1", "effectiveFrom": time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)})
	nowS := pa.Must(200, "GET", "/api/v1/billing/payment-method-settings", nil).Items()
	if len(nowS) != 1 || nowS[0]["enabled"] != true {
		t.Fatalf("current availability: %v", nowS)
	}
	laterS := pa.Must(200, "GET", "/api/v1/billing/payment-method-settings?at="+future.Add(time.Hour).Format(time.RFC3339), nil).Items()
	if len(laterS) != 1 || laterS[0]["enabled"] != false {
		t.Fatalf("future availability: %v", laterS)
	}
	if len(pa.Must(200, "GET", "/api/v1/billing/payment-method-settings?history=true", nil).Items()) != 2 {
		t.Fatal("history")
	}
	sa.Must(204, "DELETE", "/api/v1/billing/payment-methods/"+str(pm["id"]), nil)
}

// FR-MD-05: foundation entities have schema, CRUD API, unique code per
// property and status.
func TestFoundationEntities(t *testing.T) {
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	cases := []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/crm/customers", map[string]any{"code": "CORP-01", "name": "PT Corporate", "customerType": "corporate"}},
		{"/api/v1/crm/guests", map[string]any{"code": "GST-01", "name": "Guest One", "idNumber": "3171234567890001"}},
		{"/api/v1/membership/members", map[string]any{"code": "M/2026/001", "name": "Member One", "membershipType": "Family"}},
		{"/api/v1/commercial/products", map[string]any{"code": "BALL-50", "name": "Range Balls 50", "category": "Driving Range", "unit": "basket"}},
		{"/api/v1/commercial/outlets", map[string]any{"code": "HALFWAY", "name": "Halfway House", "outletType": "restaurant"}},
		{"/api/v1/sportclub/facilities", map[string]any{"code": "TENNIS-1", "name": "Tennis Court 1", "facilityType": "tennis", "capacity": 4}},
		{"/api/v1/reservation/resources", map[string]any{"code": "SUITE-1", "name": "VIP Suite 1", "resourceType": "vip_suite", "capacity": 10}},
		{"/api/v1/procurement/suppliers", map[string]any{"code": "SUP-01", "name": "PT Supplier", "npwp": "012345678901000"}},
	}
	for _, c := range cases {
		x := pa.Must(201, "POST", c.path, c.body).JSON()
		pa.Must(422, "POST", c.path, c.body) // duplicate code
		pa.Must(200, "PATCH", c.path+"/"+str(x["id"]), map[string]any{"status": "inactive"})
		pa.Must(200, "GET", c.path+"/"+str(x["id"]), nil)
		pa.Must(200, "GET", c.path+"?filter[status]=inactive", nil)
		pa.Must(200, "GET", c.path+":export?format=csv", nil)
		pa.Must(204, "DELETE", c.path+"/"+str(x["id"]), nil)
		// The same code is allowed in another property.
		other := superAdmin(t, inst)
		other.Property = inst.MDR
		other.Must(201, "POST", c.path, c.body)
	}
}

// FR-JOB-06: the same Idempotency-Key returns the same result once.
func TestIdempotencyKey(t *testing.T) {
	pa := login(t, inst, "property.admin@demo.oneclub.id", demoPassword)
	body := map[string]any{"code": "IDEM-1", "name": "Idempotent Supplier"}
	a := pa.Must(201, "POST", "/api/v1/procurement/suppliers", body, "Idempotency-Key", "key-123")
	b := pa.Must(201, "POST", "/api/v1/procurement/suppliers", body, "Idempotency-Key", "key-123")
	if string(a.Body) != string(b.Body) || b.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay mismatch:\n%s\n%s", a, b)
	}
	pa.Must(422, "POST", "/api/v1/procurement/suppliers", map[string]any{"code": "IDEM-2", "name": "Other"}, "Idempotency-Key", "key-123")
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM procurement.suppliers WHERE code = 'IDEM-1'`, nil, &n)
	if n != 1 {
		t.Fatalf("expected one supplier, got %d", n)
	}
}

// Remaining master data mutations and branding upload (FR-BRD-01).
func TestUnusedDeletesAndBrandingUpload(t *testing.T) {
	sa := superAdmin(t, inst)
	d := sa.Must(201, "POST", "/api/v1/platform/departments", map[string]any{"code": "TMP-DEPT", "name": "Temporary"}).JSON()
	sa.Must(204, "DELETE", "/api/v1/platform/departments/"+str(d["id"]), nil)
	platformAdmin(t, inst).Must(200, "PUT", "/api/v1/platform/feature-flags/platform.venue_activation_approval", map[string]any{"value": false})
	v := sa.Must(201, "POST", "/api/v1/platform/venues", map[string]any{"code": "POPUP", "name": "Pop-up Venue", "venueType": "other"}).JSON()
	if v["status"] != "active" {
		t.Fatalf("without activation approval a venue starts active: %v", v)
	}
	sa.Must(200, "PATCH", "/api/v1/platform/venues/"+str(v["id"]), map[string]any{"name": "Pop-up Venue 2", "description": "Seasonal"})
	sa.Must(204, "DELETE", "/api/v1/platform/venues/"+str(v["id"]), nil)
	platformAdmin(t, inst).Must(200, "PUT", "/api/v1/platform/feature-flags/platform.venue_activation_approval", map[string]any{"value": true})

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")
	body, ctype := multipartBody(t, nil, "file", "logo.png", string(png))
	r := sa.Do("POST", "/api/v1/platform/files", body, "Content-Type", ctype)
	if r.Status != 201 {
		t.Fatalf("upload: %s", r)
	}
	url := str(r.JSON()["url"])
	sa.Must(200, "PATCH", "/api/v1/platform/branding", map[string]any{"logoUrl": url})
	img := anon(t, inst).Must(200, "GET", url, nil)
	if img.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("public logo: %s", img.Header)
	}
	bad, bctype := multipartBody(t, nil, "file", "evil.html", "<script>alert(1)</script>")
	if r := sa.Do("POST", "/api/v1/platform/files", bad, "Content-Type", bctype); r.Status != 422 {
		t.Fatalf("html upload must be rejected: %s", r)
	}
}
