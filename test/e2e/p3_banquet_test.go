package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/outbox"
)

func init() {
	resourceCRUDModules["banquet"] = true
	resourceCRUDSkip["banquet.menu_item"] = "category must belong to the same menu; covered by TestP3BanquetWedding"
	resourceCRUDSkip["banquet.venue"] = "bookable resource sync and parts; covered by TestP3BanquetVenueHolds"
}

// bqDay is a local day n days from today at hh:mm.
func bqDay(n, hh, mm int) time.Time {
	loc := clubLoc(inst)
	d := time.Now().In(loc).AddDate(0, 0, n)
	return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, loc)
}

// bqDispatch runs the outbox until cond holds.
func bqDispatch(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitFor(t, 20*time.Second, what, func() bool {
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		return cond()
	})
}

// bqEq asserts a decimal amount.
func bqEq(t *testing.T, what string, got any, want string) {
	t.Helper()
	if !bilDec(got).Equal(decimal.RequireFromString(want)) {
		t.Fatalf("%s: got %v, want %s", what, got, want)
	}
}

// bqType creates an event type.
func bqType(t *testing.T, c *Client, code, category string) string {
	t.Helper()
	return idOf(c.Must(201, "POST", "/api/v1/banquet/event-types", map[string]any{"code": code, "name": code + " event", "category": category}))
}

// bqVenue creates a venue (its bookable resource follows).
func bqVenue(t *testing.T, c *Client, body map[string]any) map[string]any {
	t.Helper()
	v := c.Must(201, "POST", "/api/v1/banquet/venues", body).JSON()
	if v["resourceId"] == nil {
		t.Fatalf("venue without bookable resource: %v", v)
	}
	return v
}

// bqHold is the hold of a venue on an event (the live one first).
func bqHold(d map[string]any, venue string) map[string]any {
	var last map[string]any
	for _, h := range d["venues"].([]any) {
		hm := h.(map[string]any)
		if str(hm["venueId"]) != venue {
			continue
		}
		switch hm["status"] {
		case "tentative", "definite", "waitlisted", "completed":
			return hm
		}
		last = hm
	}
	return last
}

func bqNotified(t *testing.T, user, event string) int {
	t.Helper()
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications WHERE user_id = $1 AND event_code = $2`, []any{mustUUID(user), event}, &n)
	return n
}

// EP-15 (FR-VEN-01..05) and FR-EVT-01/03/10: venues with layout capacities
// on the Reservation Engine, combined venues locking their parts, the outdoor
// minimum pax, tentative holds with option date, the waitlisted second hold
// promoted (and its sales told) when the first expires, the venue calendar
// and availability, and the event list / calendar.
func TestP3BanquetVenueHolds(t *testing.T) {
	sa := superAdmin(t, inst)
	bs := roleUser(t, inst, "banquet_sales")
	sx := roleUser(t, inst, "sales_executive")
	sxID := slsUserID(t, "role.sales_executive@matrix.test")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	typ := bqType(t, sa, "VH"+sfx, "banquet")
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "BQV" + sfx, "name": "Venue Holder " + sfx, "email": "vh" + sfx + "@bq.test"}))

	hall := bqVenue(t, sa, map[string]any{"code": "VH" + sfx, "name": "Hall " + sfx, "venueType": "ballroom", "maxCapacity": 400})
	partA := bqVenue(t, sa, map[string]any{"code": "VHA" + sfx, "name": "Hall A " + sfx, "venueType": "ballroom", "maxCapacity": 200, "parentVenueId": hall["id"]})
	bqVenue(t, sa, map[string]any{"code": "VHB" + sfx, "name": "Hall B " + sfx, "venueType": "ballroom", "maxCapacity": 200, "parentVenueId": hall["id"]})
	// a part of a part is refused (one level of combination)
	sa.Must(422, "POST", "/api/v1/banquet/venues", map[string]any{"code": "VHX" + sfx, "name": "Nested", "parentVenueId": partA["id"]})
	sa.Must(422, "PATCH", "/api/v1/banquet/venues/"+str(hall["id"]), map[string]any{"parentVenueId": partA["id"]})
	upd := sa.Must(200, "PATCH", "/api/v1/banquet/venues/"+str(hall["id"]), map[string]any{"description": "Grand hall", "facilities": []string{"stage", "LED"}}).JSON()
	if upd["resourceId"] != hall["resourceId"] {
		t.Fatalf("venue update keeps its resource: %v", upd)
	}
	var rtype string
	sysQueryRow(t, inst, `SELECT resource_type FROM reservation.resources WHERE id = $1`, []any{mustUUID(str(hall["resourceId"]))}, &rtype)
	if rtype != "banquet_venue" {
		t.Fatalf("resource type: %s", rtype)
	}
	sa.Must(201, "POST", "/api/v1/banquet/venue-layouts", map[string]any{"venueId": hall["id"], "layout": "round_table", "capacity": 300})
	sa.Must(201, "POST", "/api/v1/banquet/venue-layouts", map[string]any{"venueId": hall["id"], "layout": "theater", "capacity": 400})
	garden := bqVenue(t, sa, map[string]any{"code": "VG" + sfx, "name": "Garden " + sfx, "venueType": "outdoor", "maxCapacity": 500, "minPax": 100,
		"addonPrice": "15000000"})
	// the generic CRUD skips venues: delete is refused (venues are archived)
	if r := sa.Do("DELETE", "/api/v1/banquet/venues/"+str(garden["id"]), nil); r.Status < 400 {
		t.Fatalf("venue delete: %s", r)
	}

	day := bqDay(60, 10, 0)
	avail := sa.Must(200, "GET", fmt.Sprintf("/api/v1/banquet/venue-availability?start=%s&end=%s&pax=250&layout=round_table",
		urlq(rfc(day)), urlq(rfc(day.Add(5*time.Hour)))), nil).Items()
	for _, a := range avail {
		if a["venueId"] == hall["id"] && (a["available"] != true || a["fits"] != true) {
			t.Fatalf("hall availability: %v", a)
		}
	}

	// Sales 1 holds the hall: Tentative until the option date.
	ev := func(c *Client, title string, pax int, venues []map[string]any) map[string]any {
		return c.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": title, "eventTypeId": typ, "customerId": cust, "start": rfc(day),
			"end": rfc(day.Add(5 * time.Hour)), "expectedPax": pax, "layout": "round_table", "venues": venues}).JSON()
	}
	a := ev(bs, "Gala A "+sfx, 250, []map[string]any{{"venueId": hall["id"], "functionName": "Reception"}})
	ha := bqHold(a, str(hall["id"]))
	if a["status"] != "tentative" || ha == nil || ha["status"] != "tentative" || ha["optionDate"] == nil || a["optionDate"] == nil {
		t.Fatalf("tentative hold: %v", a)
	}
	// over capacity of the layout, and the part of a held hall
	sx.Must(422, "POST", "/api/v1/banquet/events", map[string]any{"title": "Too big", "eventTypeId": typ, "customerId": cust, "start": rfc(day),
		"end": rfc(day.Add(time.Hour)), "expectedPax": 350, "layout": "round_table", "venues": []map[string]any{{"venueId": hall["id"]}}})
	sx.Must(409, "POST", "/api/v1/banquet/events", map[string]any{"title": "Part", "eventTypeId": typ, "customerId": cust, "start": rfc(day),
		"end": rfc(day.Add(time.Hour)), "expectedPax": 50, "venues": []map[string]any{{"venueId": partA["id"]}}})
	// Sales 2: the second hold on the same date only as waitlist (EP-15 AC).
	b := ev(sx, "Gala B "+sfx, 200, nil)
	sx.Must(409, "POST", "/api/v1/banquet/events/"+str(b["id"])+"/venues", map[string]any{"venueId": hall["id"]})
	hb := sx.Must(201, "POST", "/api/v1/banquet/events/"+str(b["id"])+"/venues", map[string]any{"venueId": hall["id"], "waitlist": true}).JSON()
	if hb["status"] != "waitlisted" || hb["waitlistRank"].(float64) != 1 || hb["reservationId"] != nil {
		t.Fatalf("waitlisted hold: %v", hb)
	}
	// Outdoor add-on not selectable below 100 pax (FR-BQT-05 AC: 80 pax).
	small := ev(sx, "Small garden "+sfx, 80, nil)
	r := sx.Do("POST", "/api/v1/banquet/events/"+str(small["id"])+"/venues", map[string]any{"venueId": garden["id"]})
	if r.Status != 422 || !strings.Contains(string(r.Body), "below_minimum_pax") {
		t.Fatalf("garden for 80 pax: %s", r)
	}
	// Venue calendar shows the tentative and the waitlisted hold.
	cal := sa.Must(200, "GET", "/api/v1/banquet/venue-calendar?from="+day.Format("2006-01-02")+"&to="+day.Format("2006-01-02"), nil).Items()
	found := 0
	for _, row := range cal {
		if row["venueId"] != hall["id"] {
			continue
		}
		for _, e := range row["events"].([]any) {
			if em := e.(map[string]any); em["eventId"] == a["id"] || em["eventId"] == b["id"] {
				found++
			}
		}
		if len(row["bookings"].([]any)) == 0 {
			t.Fatalf("booking calendar entries of the hall: %v", row)
		}
	}
	if found != 2 {
		t.Fatalf("venue calendar: %v", cal)
	}
	// Event list / calendar per period and status.
	list := sa.Must(200, "GET", "/api/v1/banquet/events?from="+day.Format("2006-01-02")+"&to="+day.Format("2006-01-02")+"&filter[status]=tentative,inquiry", nil).Items()
	if !containsID(list, str(a["id"])) || !containsID(list, str(b["id"])) {
		t.Fatalf("event calendar: %v", list)
	}

	// Extend the option, then let it pass: hold expires, waitlist moves up, sales 2 told.
	opt := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	if x := bs.Must(200, "POST", "/api/v1/banquet/events/"+str(a["id"])+":extend-option", map[string]any{"optionDate": rfc(opt), "reason": "Client asked"}).JSON(); x["optionDate"] == nil {
		t.Fatalf("extended: %v", x)
	}
	sysExec(t, inst, `UPDATE banquet.events SET option_date = now() - interval '1 minute' WHERE id = $1`, mustUUID(str(a["id"])))
	sysExec(t, inst, `UPDATE banquet.event_venues SET option_date = now() - interval '1 minute' WHERE event_id = $1`, mustUUID(str(a["id"])))
	before := bqNotified(t, sxID, "banquet.waitlist_promoted")
	if n, err := inst.App.Banquet.Module.ExpireOptions(t.Context()); err != nil || n < 1 {
		t.Fatalf("expire options: %d %v", n, err)
	}
	a = sa.Must(200, "GET", "/api/v1/banquet/events/"+str(a["id"]), nil).JSON()
	if a["status"] != "inquiry" || bqHold(a, str(hall["id"]))["status"] != "expired" {
		t.Fatalf("expired hold: %v", a)
	}
	b = sa.Must(200, "GET", "/api/v1/banquet/events/"+str(b["id"]), nil).JSON()
	if hb2 := bqHold(b, str(hall["id"])); b["status"] != "tentative" || hb2["status"] != "tentative" || hb2["reservationId"] == nil || hb2["promotedAt"] == nil {
		t.Fatalf("waitlist promoted: %v", b)
	}
	if bqNotified(t, sxID, "banquet.waitlist_promoted") <= before {
		t.Fatal("the sales of the waitlisted hold was not told")
	}
	// Release the promoted hold: the event goes back to inquiry.
	rel := sx.Must(200, "POST", "/api/v1/banquet/events/"+str(b["id"])+"/venues/"+str(bqHold(b, str(hall["id"]))["id"])+":release",
		map[string]any{"reason": "Client chose another date"}).JSON()
	if rel["status"] != "inquiry" {
		t.Fatalf("released: %v", rel)
	}
	// ETag / If-Match on the event header.
	g := sa.Must(200, "GET", "/api/v1/banquet/events/"+str(b["id"]), nil)
	etag := g.Header.Get("ETag")
	if etag == "" {
		t.Fatal("event without ETag")
	}
	sa.Must(200, "PATCH", "/api/v1/banquet/events/"+str(b["id"]), map[string]any{"notes": "Prefers the garden", "expectedPax": 180}, "If-Match", etag)
	sa.Must(412, "PATCH", "/api/v1/banquet/events/"+str(b["id"]), map[string]any{"notes": "stale"}, "If-Match", etag)
}

func urlq(s string) string { return strings.ReplaceAll(s, "+", "%2B") }

func outboxEvent(typ string, payload []byte, property uuid.UUID) outbox.Event {
	return outbox.Event{ID: uuid.New(), Type: typ, PropertyID: &property, Payload: payload, OccurredAt: time.Now()}
}

// bqTax creates the 15.5% tax & service of banquet (service 5%, PB1 10%
// on net + service) and returns their codes.
func bqTax(t *testing.T, sa *Client, sfx string) []string {
	t.Helper()
	svc, pb1 := "BQS"+sfx, "BQT"+sfx
	sa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": svc, "name": "Service 5% " + sfx, "kind": "service", "ratePercent": "5",
		"basis": "net_amount", "pricingMode": "plus_plus", "effectiveFrom": past()})
	sa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": pb1, "name": "PB1 10% " + sfx, "kind": "tax", "ratePercent": "10",
		"basis": "net_plus_service", "pricingMode": "plus_plus", "effectiveFrom": past()})
	return []string{svc, pb1}
}

func bqCharges(t *testing.T, c *Client, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ch := range c.Must(200, "GET", "/api/v1/banquet/events/"+event+"/billing", nil).JSON()["charges"].([]any) {
		if cm := ch.(map[string]any); cm["status"] == "posted" {
			out = append(out, cm)
		}
	}
	return out
}

func bqCharge(list []map[string]any, kind string) map[string]any {
	for _, c := range list {
		if c["kind"] == kind {
			return c
		}
	}
	return nil
}

// EP-13 (FR-BQT-01..12) and EP-14 (FR-BEO-01..06) acceptance — the wedding
// flow: Wedding Rp88.000.000 nett with the DP 30% = Rp26.400.000 and the
// settlement Rp61.600.000 due H-7 (reminders to the sales), Definite on the
// DP with the package inclusions bundled (bungalow suite night, F&B
// vouchers), 20 additional pax × Rp190.000++ = Rp4.389.000, menu quotas,
// corkage, vendors, resources, food tasting with changes carried into the
// BEO, BEO v1 → v2 (allergy) with changes marked and the kitchen's
// confirmation per version, Banquet Production and the K1 requirement from
// the BOM per pax, final pax above the guarantee charged, BEO locked after
// completion, final billing with the DP applied, and Banquet Revenue equal
// on the dashboard and the report.
func TestP3BanquetWedding(t *testing.T) {
	sa := superAdmin(t, inst)
	bs := roleUser(t, inst, "banquet_sales")
	bsID := slsUserID(t, "role.banquet_sales@matrix.test")
	kitchen := roleUser(t, inst, "kitchen_staff")
	bm := roleUser(t, inst, "banquet_manager")
	gm := roleUser(t, inst, "general_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	taxes := bqTax(t, sa, sfx)
	typ := bqType(t, sa, "WD"+sfx, "wedding")
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "BQW" + sfx, "name": "Andi & Sari " + sfx,
		"email": "andi" + sfx + "@wedding.test", "phone": "+62822" + sfx}))
	hall := bqVenue(t, sa, map[string]any{"code": "WB" + sfx, "name": "Wedding Ballroom " + sfx, "venueType": "ballroom", "maxCapacity": 500})
	sa.Must(201, "POST", "/api/v1/banquet/venue-layouts", map[string]any{"venueId": hall["id"], "layout": "round_table", "capacity": 400})
	resourceOf(t, sa, "BQBG"+sfx, "bungalow", nil, nil)
	room := resourceOf(t, sa, "BQMR"+sfx, "meeting_room", intp(30), nil)
	vt := "BQFNB" + sfx
	sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": vt, "name": "F&B voucher " + sfx, "kind": "value", "category": "fnb",
		"unit": "rupiah", "faceValue": "500000", "price": "500000", "validityMonths": 6})

	// BOM: 0.2 kg beef per portion of rendang.
	kg := idOf(sa.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": "KG" + sfx, "name": "Kilogram", "kind": "mass"}))
	beef := idOf(sa.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "BEEF" + sfx, "name": "Beef " + sfx, "baseUomId": kg}))
	rec := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "RND" + sfx, "name": "Rendang " + sfx}))
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": rec, "itemId": beef, "quantity": "0.2", "uomId": kg})

	// Menu with category quotas (2 appetizers, extra choice Rp25.000/pax; 1 dessert, no extras).
	menu := idOf(sa.Must(201, "POST", "/api/v1/banquet/menus", map[string]any{"code": "WM" + sfx, "name": "Wedding Buffet " + sfx, "menuType": "buffet",
		"pricePerPax": "190000", "taxCodes": taxes}))
	app := idOf(sa.Must(201, "POST", "/api/v1/banquet/menu-categories", map[string]any{"menuId": menu, "name": "Appetizer", "quota": 2, "extraChoicePrice": "25000"}))
	des := idOf(sa.Must(201, "POST", "/api/v1/banquet/menu-categories", map[string]any{"menuId": menu, "name": "Dessert", "quota": 1}))
	main := idOf(sa.Must(201, "POST", "/api/v1/banquet/menu-categories", map[string]any{"menuId": menu, "name": "Beef", "quota": 1}))
	item := func(cat, name string, extra map[string]any) string {
		body := map[string]any{"menuId": menu, "categoryId": cat, "name": name, "station": "buffet", "serveOffsetMinutes": 60}
		for k, v := range extra {
			body[k] = v
		}
		return idOf(sa.Must(201, "POST", "/api/v1/banquet/menu-items", body))
	}
	a1, a2, a3 := item(app, "Gado-gado", nil), item(app, "Lumpia", nil), item(app, "Caesar Salad", nil)
	d1, d2 := item(des, "Es Campur", nil), item(des, "Pudding", nil)
	rendang := item(main, "Rendang", map[string]any{"recipeId": rec, "allergens": []string{"coconut"}})
	sa.Must(200, "PATCH", "/api/v1/banquet/menu-items/"+d2, map[string]any{"description": "Chocolate"})
	other := idOf(sa.Must(201, "POST", "/api/v1/banquet/menus", map[string]any{"code": "OM" + sfx, "name": "Other " + sfx}))
	otherCat := idOf(sa.Must(201, "POST", "/api/v1/banquet/menu-categories", map[string]any{"menuId": other, "name": "X", "quota": 1}))
	sa.Must(422, "POST", "/api/v1/banquet/menu-items", map[string]any{"menuId": menu, "categoryId": otherCat, "name": "Wrong menu"})
	tmp := item(des, "Temporary", nil)
	sa.Must(204, "DELETE", "/api/v1/banquet/menu-items/"+tmp, nil)

	// Package: Wedding Rp88 jt nett for 300 pax + bungalow suite + F&B vouchers.
	sa.Must(422, "POST", "/api/v1/banquet/packages", map[string]any{"code": "WX" + sfx, "name": "Bad", "price": "1",
		"inclusions": []map[string]any{{"kind": "resource", "label": "no type"}}})
	pkg := idOf(sa.Must(201, "POST", "/api/v1/banquet/packages", map[string]any{"code": "WED" + sfx, "name": "Wedding " + sfx, "category": "wedding",
		"pricingMethod": "fixed", "price": "88000000", "pricingMode": "nett", "taxCodes": taxes, "includedPax": 300, "extraPaxPrice": "190000",
		"extraPaxMode": "plus_plus", "durationHours": 5, "menuId": menu, "inclusions": []map[string]any{
			{"kind": "resource", "label": "Bungalow suite (1 night)", "resourceType": "bungalow", "nights": 1},
			{"kind": "voucher", "label": "F&B voucher", "voucherTypeCode": vt, "quantity": 2},
			{"kind": "service", "label": "Food tasting"}}}))
	cork := idOf(sa.Must(201, "POST", "/api/v1/banquet/charge-types", map[string]any{"code": "CK" + sfx, "name": "Corkage " + sfx, "kind": "corkage",
		"unit": "bottle", "unitPrice": "150000", "pricingMode": "nett"}))
	partner := idOf(sa.Must(201, "POST", "/api/v1/banquet/vendors", map[string]any{"code": "VD" + sfx, "name": "Decor " + sfx, "vendorType": "decoration",
		"partner": true, "phone": "+62811" + sfx}))
	tpl := idOf(sa.Must(201, "POST", "/api/v1/banquet/checklist-templates", map[string]any{"code": "WT" + sfx, "name": "Wedding tasks " + sfx, "eventTypeId": typ}))
	sa.Must(201, "POST", "/api/v1/banquet/checklist-template-items", map[string]any{"templateId": tpl, "task": "Technical meeting", "department": "banquet",
		"daysBefore": 14})
	sa.Must(201, "POST", "/api/v1/banquet/checklist-template-items", map[string]any{"templateId": tpl, "task": "Confirm final pax", "department": "sales",
		"daysBefore": 40})

	// Event with the package and the ballroom.
	day := bqDay(30, 11, 0)
	ev := bs.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": "Wedding Andi & Sari " + sfx, "eventTypeId": typ, "customerId": cust,
		"start": rfc(day), "end": rfc(day.Add(5 * time.Hour)), "expectedPax": 300, "layout": "round_table", "packageId": pkg, "powerWatt": 15000,
		"venues": []map[string]any{{"venueId": hall["id"], "functionName": "Reception"}}, "specialRequests": "Halal kitchen"}).JSON()
	eid := str(ev["id"])
	bqEq(t, "contract total", ev["contractTotal"], "88000000")
	if ev["status"] != "tentative" || len(ev["inclusions"].([]any)) != 3 || ev["checklist"].(map[string]any)["total"].(float64) != 2 {
		t.Fatalf("wedding event: %v", ev)
	}
	for _, in := range ev["inclusions"].([]any) {
		im := in.(map[string]any)
		if (im["kind"] == "resource" && im["status"] != "held") || (im["kind"] == "voucher" && im["status"] != "pending") {
			t.Fatalf("inclusion before definite: %v", im)
		}
	}
	// Payment schedule (Banquet Policies): DP 30% and the settlement H-7.
	bl := bs.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/payment-schedule", map[string]any{}).JSON()
	lines := bl["schedule"].(map[string]any)["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("schedule: %v", bl["schedule"])
	}
	dp, final := lines[0].(map[string]any), lines[1].(map[string]any)
	bqEq(t, "DP 30%", dp["amount"], "26400000")
	bqEq(t, "settlement", final["amount"], "61600000")
	if final["dueDate"] != day.AddDate(0, 0, -7).Format("2006-01-02") || dp["kind"] != "down_payment" {
		t.Fatalf("due dates: %v", lines)
	}
	bs.Must(409, "POST", "/api/v1/banquet/events/"+eid+"/payment-schedule", map[string]any{})
	if r := bs.Do("POST", "/api/v1/banquet/events/"+eid+":make-definite", map[string]any{}); r.Status != 409 || !strings.Contains(string(r.Body), "deposit_required") {
		t.Fatalf("definite before the DP: %s", r)
	}
	// Reminders of the settlement go to the sales (D-14 of the due date).
	sid := str(bl["schedule"].(map[string]any)["id"])
	sysExec(t, inst, `UPDATE billing.payment_schedule_lines SET due_date = $2::date WHERE id = $1`, mustUUID(str(final["id"])),
		time.Now().In(clubLoc(inst)).AddDate(0, 0, 14).Format("2006-01-02"))
	before := bqNotified(t, bsID, "banquet.payment_due")
	ctx := dbtx.System(t.Context())
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := inst.App.BillingHTTP.ScheduleReminders(ctx, tx, inst.Main)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	bqDispatch(t, "settlement reminder to the sales", func() bool { return bqNotified(t, bsID, "banquet.payment_due") > before })
	// DP paid (bank transfer) → Definite: venue confirmed, inclusions delivered.
	sa.Must(201, "POST", "/api/v1/billing/payment-schedules/"+sid+"/lines/"+str(dp["id"])+":pay", map[string]any{"methodType": "bank_transfer",
		"reference": "TRF-DP-" + sfx})
	bqDispatch(t, "definite on the DP", func() bool {
		return sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()["status"] == "definite"
	})
	ev = sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()
	if bqHold(ev, str(hall["id"]))["status"] != "definite" || ev["billing"].(map[string]any)["downPaymentReceived"] != true {
		t.Fatalf("definite: %v", ev)
	}
	for _, in := range ev["inclusions"].([]any) {
		im := in.(map[string]any)
		if im["kind"] == "voucher" && (im["status"] != "issued" || len(im["voucherCodes"].([]any)) != 2) {
			t.Fatalf("vouchers issued on definite: %v", im)
		}
	}
	for _, r := range ev["resources"].([]any) {
		if rm := r.(map[string]any); rm["inclusionId"] != nil && rm["status"] != "confirmed" {
			t.Fatalf("bungalow suite confirmed with the event: %v", rm)
		}
	}

	// 20 additional pax × Rp190.000++ = Rp4.389.000 (FR-BQT-03 AC).
	bs.Must(200, "POST", "/api/v1/banquet/events/"+eid+":guarantee-pax", map[string]any{"pax": 320, "reason": "Final guest list"})
	ch := bqCharges(t, bs, eid)
	bqEq(t, "additional pax", bqCharge(ch, "extra_pax")["total"], "4389000")
	bqEq(t, "package", bqCharge(ch, "package")["total"], "88000000")
	// Decrease beyond the policy share is refused.
	bs.Must(422, "POST", "/api/v1/banquet/events/"+eid+":guarantee-pax", map[string]any{"pax": 200})

	// Menu selection: 3 appetizers (1 extra choice charged per pax), 2 desserts refused.
	r := bs.Do("PUT", "/api/v1/banquet/events/"+eid+"/menu-selection", map[string]any{"menuId": menu, "itemIds": []string{a1, d1, d2}})
	if r.Status != 422 || !strings.Contains(string(r.Body), "quota_exceeded") {
		t.Fatalf("over the dessert quota: %s", r)
	}
	ev = bs.Must(200, "PUT", "/api/v1/banquet/events/"+eid+"/menu-selection", map[string]any{"menuId": menu, "itemIds": []string{a1, a2, a3, d1, rendang}}).JSON()
	bqEq(t, "extra appetizer for 320 pax", bqCharge(bqCharges(t, bs, eid), "extra_choice")["total"], "9240000")
	if m := ev["menus"].([]any)[0].(map[string]any); len(m["items"].([]any)) != 5 {
		t.Fatalf("selection: %v", m)
	}
	// Corkage (2 bottles), a damage charge voided, a vendor and a resource.
	ck := bs.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/charges", map[string]any{"chargeTypeId": cork, "quantity": "2"}).JSON()
	bqEq(t, "corkage", ck["total"], "300000")
	bs.Must(422, "POST", "/api/v1/banquet/events/"+eid+"/charges", map[string]any{"kind": "corkage", "outsideFood": true})
	dmg := bs.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/charges", map[string]any{"kind": "damage", "unitPrice": "500000", "description": "Broken glass"}).JSON()
	bs.Must(422, "POST", "/api/v1/banquet/event-charges/"+str(dmg["id"])+":void", map[string]any{})
	if v := bs.Must(200, "POST", "/api/v1/banquet/event-charges/"+str(dmg["id"])+":void", map[string]any{"reason": "Charged to the vendor"}).JSON(); v["status"] != "voided" {
		t.Fatalf("void: %v", v)
	}
	el := bs.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/charges", map[string]any{"kind": "electricity"}).JSON()
	if !strings.Contains(str(el["description"]), "5000 W") {
		t.Fatalf("electricity above the 10.000 W quota: %v", el)
	}
	vd := bs.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/vendors", map[string]any{"vendorId": partner, "service": "Stage decoration", "fee": "5000000",
		"chargeToCustomer": true, "arrivalAt": rfc(day.Add(-4 * time.Hour))}).JSON()
	if vd["chargeId"] == nil {
		t.Fatalf("vendor fee charged: %v", vd)
	}
	if v := bs.Must(200, "POST", "/api/v1/banquet/event-vendors/"+str(vd["id"])+":cancel", map[string]any{"reason": "Client brings own decorator"}).JSON(); v["status"] != "cancelled" {
		t.Fatalf("vendor cancelled: %v", v)
	}
	bs.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/vendors", map[string]any{"vendorId": partner, "service": "Photo booth"})
	er := bs.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/resources", map[string]any{"resourceId": room, "description": "Family room",
		"amount": "2000000", "revenueComponent": "venue_rental"}).JSON()
	if er["status"] != "confirmed" || er["reservationId"] == nil {
		t.Fatalf("family room: %v", er)
	}
	bs.Must(200, "POST", "/api/v1/banquet/event-resources/"+str(er["id"])+":release", map[string]any{"reason": "Not needed"})

	// Food tasting & technical meeting; run-of-show; checklist.
	mt := bs.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/meetings", map[string]any{"kind": "food_tasting", "scheduledAt": rfc(bqDay(10, 14, 0)),
		"attendees": []string{"Andi", "Sari"}}).JSON()
	bs.Must(200, "POST", "/api/v1/banquet/event-meetings/"+str(mt["id"])+":record", map[string]any{"outcome": "Approved", "changes": []string{"Less spicy rendang"}})
	sched := bs.Must(200, "PUT", "/api/v1/banquet/events/"+eid+"/schedule", map[string]any{"items": []map[string]any{
		{"start": rfc(day), "title": "Guest arrival", "venueId": hall["id"], "department": "front_desk"},
		{"start": rfc(day.Add(time.Hour)), "end": rfc(day.Add(3 * time.Hour)), "title": "Lunch buffet", "venueId": hall["id"], "department": "fnb_service",
			"ownerName": "Captain Budi"}}}).Items()
	if len(sched) != 2 || len(bs.Must(200, "GET", "/api/v1/banquet/events/"+eid+"/schedule", nil).Items()) != 2 {
		t.Fatalf("run-of-show: %v", sched)
	}
	cl := bs.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/checklist", map[string]any{"task": "Print seating plan", "department": "banquet",
		"dueDate": day.AddDate(0, 0, -2).Format("2006-01-02")}).JSON()
	bs.Must(200, "PATCH", "/api/v1/banquet/checklist-items/"+str(cl["id"]), map[string]any{"ownerName": "Rina", "dueDate": day.AddDate(0, 0, -3).Format("2006-01-02")})
	if x := bs.Must(200, "POST", "/api/v1/banquet/checklist-items/"+str(cl["id"])+":toggle", map[string]any{"done": true}).JSON(); x["status"] != "done" {
		t.Fatalf("task done: %v", x)
	}
	// "Confirm final pax" was due 40 days before the event: overdue now.
	overdue := bs.Must(200, "GET", "/api/v1/banquet/checklist-items?overdue=true&filter[eventId]="+eid, nil).Items()
	if len(overdue) != 1 || overdue[0]["task"] != "Confirm final pax" {
		t.Fatalf("overdue tasks: %v", overdue)
	}
	if n, err := inst.App.Banquet.Module.DailyReminders(t.Context()); err != nil || n < 1 {
		t.Fatalf("daily reminders: %d %v", n, err)
	}
	if len(bs.Must(200, "POST", "/api/v1/banquet/events/"+eid+"/checklist:apply-template", map[string]any{"templateId": tpl}).Items()) != 3 {
		t.Fatal("re-applying the template duplicates nothing")
	}

	// BEO v1: draft → edit (If-Match) → issue.
	b := bs.Must(201, "POST", "/api/v1/banquet/beos", map[string]any{"eventId": eid, "instructions": map[string]string{"kitchen": "Halal kitchen only"}}).JSON()
	bid := str(b["id"])
	if b["version"].(float64) != 1 || b["status"] != "draft" || len(b["content"].(map[string]any)["menus"].([]any)) != 1 {
		t.Fatalf("draft BEO: %v", b)
	}
	if !strings.Contains(fmt.Sprint(b["content"].(map[string]any)["meetingChanges"]), "Less spicy rendang") {
		t.Fatalf("meeting changes forwarded to the BEO: %v", b["content"])
	}
	tag := sa.Must(200, "GET", "/api/v1/banquet/beos/"+bid, nil).Header.Get("ETag")
	b = bs.Must(200, "PATCH", "/api/v1/banquet/beos/"+bid, map[string]any{"instructions": map[string]string{"engineering": "Genset 20 kW"}}, "If-Match", tag).JSON()
	bm.Must(412, "POST", "/api/v1/banquet/beos/"+bid+":issue", map[string]any{}, "If-Match", tag)
	tag = fmt.Sprintf(`"%v"`, b["rev"])
	b = bm.Must(200, "POST", "/api/v1/banquet/beos/"+bid+":issue", map[string]any{}, "If-Match", tag).JSON()
	if b["status"] != "issued" || len(b["departments"].([]any)) < 5 || len(b["requirements"].([]any)) != 1 {
		t.Fatalf("issued BEO: %v", b)
	}
	bqEq(t, "beef for 320 pax", b["requirements"].([]any)[0].(map[string]any)["quantity"], "64")
	var k1 int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'banquet.beo_issued' AND aggregate_id = $1
		AND payload->'requirements'->0->>'itemId' = $2`, []any{mustUUID(bid), beef}, &k1)
	if k1 != 1 {
		t.Fatal("banquet.beo_issued with the requirement (K1) not published")
	}
	if pr := bs.Must(200, "GET", "/api/v1/banquet/events/"+eid+"/procurement-requirement", nil).JSON(); str(pr["beoId"]) != bid {
		t.Fatalf("procurement requirement: %v", pr)
	}
	// BEO v2 (allergy): version raised, changes marked, kitchen must confirm again.
	kitchen.Must(200, "POST", "/api/v1/banquet/beos/"+bid+":acknowledge", map[string]any{"department": "kitchen"})
	tag = sa.Must(200, "GET", "/api/v1/banquet/beos/"+bid, nil).Header.Get("ETag")
	bm.Must(422, "POST", "/api/v1/banquet/beos/"+bid+":revise", map[string]any{}, "If-Match", tag)
	v2 := bm.Must(200, "POST", "/api/v1/banquet/beos/"+bid+":revise", map[string]any{"reason": "Allergy", "notes": "Table 3: nut allergy"}, "If-Match", tag).JSON()
	if v2["version"].(float64) != 2 || v2["status"] != "issued" || len(v2["changes"].([]any)) == 0 || !strings.Contains(fmt.Sprint(v2["changes"]), "notes") {
		t.Fatalf("BEO v2: %v", v2)
	}
	kitchen.Must(409, "POST", "/api/v1/banquet/beos/"+bid+":acknowledge", map[string]any{"department": "kitchen"})
	if s := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()["beo"].(map[string]any); !strings.Contains(fmt.Sprint(s["pendingDepartments"]), "kitchen") {
		t.Fatalf("the kitchen has not confirmed v2: %v", s)
	}
	kitchen.Must(200, "POST", "/api/v1/banquet/beos/"+str(v2["id"])+":acknowledge", map[string]any{"department": "kitchen"})
	if s := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()["beo"].(map[string]any); strings.Contains(fmt.Sprint(s["pendingDepartments"]), "kitchen") {
		t.Fatalf("kitchen confirmed v2: %v", s)
	}
	if list := sa.Must(200, "GET", "/api/v1/banquet/beos?filter[eventId]="+eid, nil).Items(); len(list) != 1 || list[0]["version"].(float64) != 2 {
		t.Fatalf("current BEO: %v", list)
	}
	if pdf := sa.Must(200, "GET", "/api/v1/banquet/beos/"+str(v2["id"])+"/pdf", nil); string(pdf.Body[:4]) != "%PDF" {
		t.Fatal("BEO PDF")
	}
	// Kitchen Banquet Production of the event day.
	prod := kitchen.Must(200, "GET", "/api/v1/banquet/production?date="+day.Format("2006-01-02")+"&filter[station]=buffet", nil).Items()
	var dish map[string]any
	for _, p := range prod {
		if p["eventId"] == eid && p["name"] == "Rendang" {
			dish = p
		}
	}
	if dish == nil || dish["quantity"] != "320" || dish["beoVersion"].(float64) != 2 {
		t.Fatalf("banquet production: %v", prod)
	}
	kitchen.Must(200, "POST", "/api/v1/banquet/production-items/"+str(dish["id"])+":status", map[string]any{"status": "in_progress"})

	// Event day: incident note; completion with 325 pax (5 above the guarantee charged).
	bm.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/incidents", map[string]any{"severity": "medium", "note": "Generator restarted"})
	done := bm.Must(200, "POST", "/api/v1/banquet/events/"+eid+":complete", map[string]any{"finalPax": 325}).JSON()
	if done["status"] != "completed" {
		t.Fatalf("completed: %v", done)
	}
	extra := 0
	for _, c := range bqCharges(t, bs, eid) {
		if c["kind"] == "extra_pax" && strings.Contains(str(c["description"]), "5 pax") {
			bqEq(t, "5 pax above the guarantee", c["total"], "1097250")
			extra++
		}
	}
	if extra != 1 {
		t.Fatal("pax above the guarantee not charged")
	}
	if b := sa.Must(200, "GET", "/api/v1/banquet/beos/"+str(v2["id"]), nil).JSON(); b["lockedAt"] == nil {
		t.Fatal("BEO not locked after completion")
	}
	bs.Must(409, "POST", "/api/v1/banquet/beos", map[string]any{"eventId": eid})
	var k6 int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'banquet.event_completed' AND aggregate_id = $1
		AND (payload->>'finalPax')::int = 325 AND payload->'consumption'->0->>'quantity' = '65'`, []any{mustUUID(eid)}, &k6)
	if k6 != 1 {
		t.Fatal("banquet.event_completed with the consumption (K6) not published")
	}
	// Final billing: deposits applied, balance paid, folio closed.
	fb := bs.Must(200, "POST", "/api/v1/banquet/events/"+eid+":final-billing", map[string]any{"payment": map[string]any{"methodType": "cash"}}).JSON()
	if fb["folioClosed"] != true || bilDec(fb["depositsApplied"]).Cmp(decimal.NewFromInt(26_400_000)) != 0 {
		t.Fatalf("final billing: %v", fb)
	}
	bqEq(t, "balance after the DP", fb["due"], bilDec(fb["charges"]).Sub(decimal.NewFromInt(26_400_000)).String())
	if e := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON(); e["settledAt"] == nil || e["finalBilledAt"] == nil {
		t.Fatalf("settled: %v", e)
	}
	bs.Must(409, "POST", "/api/v1/banquet/events/"+eid+":final-billing", map[string]any{})

	// Banquet Revenue: dashboard = report (EP-23 AC); Event and BEO reports.
	from, to := time.Now().AddDate(0, 0, -1).Format("2006-01-02"), time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	rev := gm.Must(200, "GET", "/api/v1/reporting/reports/banquet.revenue?params[from]="+from+"&params[to]="+to, nil).JSON()
	sum := decimal.Zero
	for _, row := range rev["rows"].([]any) {
		sum = sum.Add(bilDec(row.(map[string]any)["total"]))
	}
	dash := gm.Must(200, "GET", "/api/v1/reporting/dashboards/banquet-performance?from="+from+"&to="+to, nil).JSON()
	kpis := map[string]any{}
	for _, k := range dash["kpis"].([]any) {
		km := k.(map[string]any)
		kpis[str(km["key"])] = km["value"]
	}
	if !bilDec(kpis["banquet_revenue"]).Equal(sum) || sum.LessThan(decimal.NewFromInt(100_000_000)) {
		t.Fatalf("dashboard %v vs report %s", kpis["banquet_revenue"], sum)
	}
	for _, k := range []string{"inquiries", "quotations", "deals", "event_count", "pax", "outstanding_dp", "outstanding_settlement"} {
		if _, ok := kpis[k]; !ok {
			t.Fatalf("Banquet Performance misses %s: %v", k, kpis)
		}
	}
	er2 := gm.Must(200, "GET", "/api/v1/reporting/reports/banquet.event?params[from]="+day.Format("2006-01-02")+"&params[to]="+day.Format("2006-01-02"), nil).JSON()
	if !strings.Contains(fmt.Sprint(er2["rows"]), str(ev["number"])) {
		t.Fatalf("event report: %v", er2["rows"])
	}
	br := gm.Must(200, "GET", "/api/v1/reporting/reports/banquet.beo?params[from]="+day.Format("2006-01-02")+"&params[to]="+day.Format("2006-01-02"), nil).JSON()
	if !strings.Contains(fmt.Sprint(br["rows"]), str(v2["number"])) {
		t.Fatalf("BEO report: %v", br["rows"])
	}
}

// FR-BQT-01 AC (Social Event 25 pax billed at the minimum 30: Rp5.700.000++
// = Rp6.583.500), FR-VEN-03 Definite by hand only with the Definite without
// Deposit approval, and FR-POL-P3-01 cancellation & DP forfeiture: more than
// 90 days before the event half of the DP is kept and the rest refunded;
// holds and resources are released.
func TestP3BanquetCancellation(t *testing.T) {
	sa := superAdmin(t, inst)
	bs := roleUser(t, inst, "banquet_sales")
	gm := roleUser(t, inst, "general_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	taxes := bqTax(t, sa, sfx)
	typ := bqType(t, sa, "SC"+sfx, "social")
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "BQC" + sfx, "name": "Social Club " + sfx, "phone": "+62823" + sfx}))
	pool := bqVenue(t, sa, map[string]any{"code": "SP" + sfx, "name": "Pool Side " + sfx, "venueType": "outdoor", "maxCapacity": 200})
	social := idOf(sa.Must(201, "POST", "/api/v1/banquet/packages", map[string]any{"code": "SOC" + sfx, "name": "Social Event " + sfx, "category": "social",
		"pricingMethod": "per_pax", "price": "190000", "pricingMode": "plus_plus", "taxCodes": taxes, "minPax": 30}))

	day := bqDay(100, 18, 0)
	ev := bs.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": "Community dinner " + sfx, "eventTypeId": typ, "customerId": cust,
		"start": rfc(day), "end": rfc(day.Add(4 * time.Hour)), "expectedPax": 25, "packageId": social,
		"venues": []map[string]any{{"venueId": pool["id"]}}}).JSON()
	eid := str(ev["id"])
	pc := bqCharge(bqCharges(t, bs, eid), "package")
	bqEq(t, "30 pax minimum ++", pc["total"], "6583500")
	bqEq(t, "net of 30 × 190.000", pc["net"], "5700000")
	if !strings.Contains(str(pc["description"]), "minimum 30 pax") || ev["chargedPax"].(float64) != 30 {
		t.Fatalf("minimum pax charge: %v / %v", pc, ev["chargedPax"])
	}
	// Re-pricing the package (PUT of the same package) keeps one package charge.
	bs.Must(200, "POST", "/api/v1/banquet/events/"+eid+"/package", map[string]any{"packageId": social, "pax": 35})
	bqEq(t, "35 pax", bqCharge(bqCharges(t, bs, eid), "package")["total"], "7680750")

	// Definite by hand before the DP: override with the approval of the GM.
	sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "banquet_definite_override", "name": "Definite override " + sfx,
		"steps": []map[string]any{{"stepNo": 1, "name": "General Manager", "approverType": "role", "approverRoleId": roleID(t, sa, "general_manager")}}})
	bs.Must(422, "POST", "/api/v1/banquet/events/"+eid+":make-definite", map[string]any{"override": true})
	dr := bs.Must(200, "POST", "/api/v1/banquet/events/"+eid+":make-definite", map[string]any{"override": true, "reason": "Long-standing client"}).JSON()
	if dr["approvalStatus"] != "pending" || dr["event"].(map[string]any)["status"] != "tentative" {
		t.Fatalf("override pending: %v", dr)
	}
	gm.Must(200, "POST", "/api/v1/platform/approvals/"+str(dr["approvalRequestId"])+":approve", map[string]any{"reason": "OK"})
	ev = sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()
	if ev["status"] != "definite" || !strings.Contains(str(ev["definiteReason"]), "approved") {
		t.Fatalf("definite after the approval: %v", ev)
	}
	// DP 30% paid, then cancelled 100 days ahead: 50% of the DP forfeited.
	bl := bs.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/payment-schedule", map[string]any{"lines": []map[string]any{
		{"label": "DP 30%", "kind": "down_payment", "percent": "30", "dueDate": clubToday(inst)},
		{"label": "Final", "kind": "final", "dueDate": day.AddDate(0, 0, -7).Format("2006-01-02")}}}).JSON()
	dpl := bl["schedule"].(map[string]any)["lines"].([]any)[0].(map[string]any)
	bqEq(t, "DP", dpl["amount"], "2304225")
	sa.Must(201, "POST", "/api/v1/billing/payment-schedules/"+str(bl["schedule"].(map[string]any)["id"])+"/lines/"+str(dpl["id"])+":pay",
		map[string]any{"methodType": "cash"})
	bs.Must(422, "POST", "/api/v1/banquet/events/"+eid+":cancel", map[string]any{})
	cr := bs.Must(200, "POST", "/api/v1/banquet/events/"+eid+":cancel", map[string]any{"reason": "Venue change by the community"}).JSON()
	if cr["tier"].(map[string]any)["minDaysBefore"].(float64) != 91 {
		t.Fatalf("cancellation tier: %v", cr)
	}
	bqEq(t, "fee: half of the DP", cr["fee"], "1152113")
	bqEq(t, "refund: the other half", cr["refunded"], "1152112")
	ce := cr["event"].(map[string]any)
	if ce["status"] != "cancelled" || bqHold(ce, str(pool["id"]))["status"] != "cancelled" {
		t.Fatalf("cancelled event: %v", ce)
	}
	if c := bqCharges(t, bs, eid); len(c) != 1 || c[0]["kind"] != "cancellation" {
		t.Fatalf("only the cancellation fee remains: %v", c)
	}
	bs.Must(409, "POST", "/api/v1/banquet/events/"+eid+":cancel", map[string]any{"reason": "again"})
	var outbox int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'banquet.event_cancelled' AND aggregate_id = $1`, []any{mustUUID(eid)}, &outbox)
	if outbox != 1 {
		t.Fatal("banquet.event_cancelled not published")
	}
}

// bqQuotationEvent is the live event of a quotation number (empty: none).
func bqQuotationEvent(t *testing.T, number any) (id, status string) {
	t.Helper()
	sysQueryRow(t, inst, `SELECT coalesce((SELECT id::text FROM banquet.events WHERE quotation_number = $1 AND status <> 'cancelled'), ''),
		coalesce((SELECT status FROM banquet.events WHERE quotation_number = $1 ORDER BY created_at DESC LIMIT 1), '')`, []any{str(number)}, &id, &status)
	return id, status
}

// CRM integration (docs/p3-p4-contracts.md): crm.quotation_sent places a
// Tentative hold on the quoted venue until the option date, a revision moves
// it, crm.quotation_accepted converts the quotation into that event with the
// quoted lines as charges and the payment terms as the schedule — once, even
// when the acceptance is delivered twice (DP issued once) — and a rejected
// quotation releases its hold.
func TestP3BanquetQuotation(t *testing.T) {
	sa := superAdmin(t, inst)
	bs := roleUser(t, inst, "banquet_sales")
	bsID := slsUserID(t, "role.banquet_sales@matrix.test")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "BQQ" + sfx, "name": "Dewi & Raka " + sfx,
		"email": "dewi" + sfx + "@quote.test"}))
	hall := bqVenue(t, sa, map[string]any{"code": "QH" + sfx, "name": "Quote Hall " + sfx, "venueType": "ballroom", "maxCapacity": 400})
	rid := str(hall["resourceId"])
	loc := clubLoc(inst)
	today := time.Now().In(loc)
	day := today.AddDate(0, 3, 0)
	qin := map[string]any{"customerId": cust, "title": "Wedding Dewi & Raka " + sfx, "line": "wedding", "eventType": "wedding",
		"eventDate": day.Format("2006-01-02"), "pax": 200, "venueResourceId": rid, "optionDate": today.AddDate(0, 0, 5).Format("2006-01-02"),
		"ownerUserId": bsID, "pricingMode": "nett",
		"paymentTerms": []map[string]any{{"label": "DP 30%", "percent": "30", "dueDays": 3}, {"label": "Final Payment", "percent": "70", "dueDays": 30}},
		"lines": []map[string]any{{"itemType": "banquet_package", "description": "Wedding package 200 pax", "quantity": "1", "unitPrice": "60000000"},
			{"itemType": "service", "description": "Wedding cake", "quantity": "1", "unitPrice": "5000000"}}}
	q1 := bs.Must(201, "POST", "/api/v1/crm/quotations", qin, "Idempotency-Key", newKey()).JSON()
	bs.Must(200, "POST", "/api/v1/crm/quotations/"+str(q1["id"])+":send", map[string]any{})
	var eid string
	bqDispatch(t, "tentative hold of the sent quotation", func() bool {
		eid, _ = bqQuotationEvent(t, q1["number"])
		return eid != ""
	})
	ev := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()
	h := bqHold(ev, str(hall["id"]))
	opt := time.Date(today.Year(), today.Month(), today.Day()+6, 0, 0, 0, 0, loc)
	if ev["status"] != "tentative" || h == nil || h["status"] != "tentative" || str(ev["salesOwnerId"]) != bsID || ev["source"] != "quotation" {
		t.Fatalf("hold of the quotation: %v", ev)
	}
	if od, _ := time.Parse(time.RFC3339, str(h["optionDate"])); !od.Equal(opt) {
		t.Fatalf("hold until the end of the option date %v: %v", opt, h["optionDate"])
	}
	// Revision with another date: the hold moves (same event).
	q2 := bs.Must(200, "POST", "/api/v1/crm/quotations/"+str(q1["id"])+":revise", nil).JSON()
	qin["eventDate"] = day.AddDate(0, 0, 1).Format("2006-01-02")
	bs.Must(200, "PUT", "/api/v1/crm/quotations/"+str(q2["id"]), qin)
	bs.Must(200, "POST", "/api/v1/crm/quotations/"+str(q2["id"])+":send", map[string]any{})
	bqDispatch(t, "hold moved by the revision", func() bool {
		d := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()
		hh := bqHold(d, str(hall["id"]))
		return hh != nil && strings.HasPrefix(str(d["start"]), day.AddDate(0, 0, 1).Format("2006-01-02")) && hh["status"] == "tentative"
	})
	ev = sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()
	active := 0
	for _, x := range ev["venues"].([]any) {
		if xm := x.(map[string]any); xm["status"] == "tentative" {
			active++
		} else if xm["status"] != "released" {
			t.Fatalf("old hold: %v", xm)
		}
	}
	if active != 1 {
		t.Fatalf("one hold after the revision: %v", ev["venues"])
	}
	// Accepted → the event (charges, DP schedule), the hold kept.
	acc := bs.Must(200, "POST", "/api/v1/crm/quotations/"+str(q2["id"])+":accept", map[string]any{"acceptedByName": "Dewi"}).JSON()
	bqDispatch(t, "quotation converted", func() bool {
		var conv bool
		sysQueryRow(t, inst, `SELECT converted_at IS NOT NULL FROM banquet.events WHERE id = $1`, []any{mustUUID(eid)}, &conv)
		return conv
	})
	ev = sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()
	bqEq(t, "contract = quotation total", ev["contractTotal"], str(acc["total"]))
	if ev["scheduleId"] == nil || bqHold(ev, str(hall["id"])) == nil {
		t.Fatalf("converted event: %v", ev)
	}
	sc := sa.Must(200, "GET", "/api/v1/billing/payment-schedules/"+str(ev["scheduleId"]), nil).JSON()
	bqEq(t, "DP of the terms", sc["lines"].([]any)[0].(map[string]any)["amount"], bilDec(acc["total"]).Mul(decimal.NewFromFloat(0.3)).Round(0).String())
	// The acceptance delivered again: nothing new (DP issued once).
	var payload []byte
	sysQueryRow(t, inst, `SELECT payload FROM platform.outbox WHERE event_type = 'crm.quotation_accepted' AND aggregate_id = $1`, []any{mustUUID(str(q2["id"]))}, &payload)
	pid := inst.Main
	if err := inst.DB.WithTx(dbtx.System(t.Context()), func(tx pgx.Tx) error {
		return inst.App.Banquet.Module.OnQuotationAccepted(dbtx.System(t.Context()), tx, outboxEvent("crm.quotation_accepted", payload, pid))
	}); err != nil {
		t.Fatal(err)
	}
	var events, schedules, charges int
	sysQueryRow(t, inst, `SELECT (SELECT count(*) FROM banquet.events WHERE quotation_number = $1),
		(SELECT count(*) FROM billing.payment_schedules WHERE source_ref = (SELECT number FROM banquet.events WHERE id = $2) OR source_ref = $1),
		(SELECT count(*) FROM banquet.event_charges WHERE event_id = $2 AND status = 'posted')`, []any{str(q2["number"]), mustUUID(eid)}, &events, &schedules, &charges)
	if events != 1 || schedules != 1 || charges != 2 {
		t.Fatalf("acceptance twice: %d events, %d schedules, %d charges", events, schedules, charges)
	}

	// A rejected quotation releases its hold.
	qin["title"], qin["line"], qin["eventDate"] = "Banquet "+sfx, "banquet", day.AddDate(0, 0, 10).Format("2006-01-02")
	r1 := bs.Must(201, "POST", "/api/v1/crm/quotations", qin, "Idempotency-Key", newKey()).JSON()
	bs.Must(200, "POST", "/api/v1/crm/quotations/"+str(r1["id"])+":send", map[string]any{})
	var rid2 string
	bqDispatch(t, "hold of the second quotation", func() bool {
		rid2, _ = bqQuotationEvent(t, r1["number"])
		return rid2 != ""
	})
	bs.Must(200, "POST", "/api/v1/crm/quotations/"+str(r1["id"])+":reject", map[string]any{"reason": "Over budget"})
	bqDispatch(t, "hold released", func() bool {
		_, st := bqQuotationEvent(t, r1["number"])
		return st == "cancelled"
	})
	d := sa.Must(200, "GET", "/api/v1/banquet/events/"+rid2, nil).JSON()
	if h := bqHold(d, str(hall["id"])); h == nil || h["status"] != "cancelled" {
		t.Fatalf("released hold: %v", d)
	}

	// commercial.package_booked: the banquet component becomes a Definite event on the venue, once.
	comp := uuid.NewString()
	pkb, _ := json.Marshal(map[string]any{"bookingId": uuid.NewString(), "number": "PKB-" + sfx, "packageCode": "WED-GOLD", "customerId": cust,
		"startDate": day.AddDate(0, 0, 20).Format("2006-01-02"), "endDate": day.AddDate(0, 0, 20).Format("2006-01-02"), "pax": 120,
		"components": []map[string]any{{"bookingComponentId": comp, "componentType": "banquet", "allocationRef": rid,
			"serviceDate": day.AddDate(0, 0, 20).Format("2006-01-02"), "quantity": "1"}, {"bookingComponentId": uuid.NewString(), "componentType": "reservation"}}})
	for range 2 {
		if err := inst.DB.WithTx(dbtx.System(t.Context()), func(tx pgx.Tx) error {
			return inst.App.Banquet.Module.OnPackageBooked(dbtx.System(t.Context()), tx, outboxEvent("commercial.package_booked", pkb, inst.Main))
		}); err != nil {
			t.Fatal(err)
		}
	}
	var pkgEvents int
	var pkgStatus string
	sysQueryRow(t, inst, `SELECT count(*), max(status) FROM banquet.events WHERE package_component_id = $1`, []any{mustUUID(comp)}, &pkgEvents, &pkgStatus)
	if pkgEvents != 1 || pkgStatus != "definite" {
		t.Fatalf("package banquet: %d events, %s", pkgEvents, pkgStatus)
	}
}

// FR-EVT-04, FR-WEB-P3-01/03, FR-APP-P3-03, FR-OPS-P3-01/05, FR-POL-P3-05:
// open events on the website and in the Member App, Book Event with the
// online payment of the fee (gateway webhook settles it), member
// registration with the QR ticket and My Events, capacity and waitlist with
// automatic promotion when a member withdraws, guest list import, Today's
// Events, QR check-in at the door (repeat scan safe) and the offline
// check-in queue synced idempotently (a withdrawn ticket is a conflict).
func TestP3BanquetRegistration(t *testing.T) {
	sa := superAdmin(t, inst)
	staff := roleUser(t, inst, "event_staff")
	mc := roleUser(t, inst, "member")
	me := memberCustomer(t, sa)
	pub := anon(t, inst)
	pub.Property = uuid.Nil
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	typ := bqType(t, sa, "OE"+sfx, "social")
	host := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "BQO" + sfx, "name": "Club Committee " + sfx, "phone": "+62824" + sfx}))
	mk := func(title string, start time.Time, capacity int, fee string) string {
		e := sa.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": title, "eventTypeId": typ, "customerId": host, "start": rfc(start),
			"end": rfc(start.Add(4 * time.Hour)), "expectedPax": capacity, "public": true, "registrationOpen": true, "capacity": capacity,
			"registrationFee": fee, "description": "Open to members and guests"}).JSON()
		if d := sa.Must(200, "POST", "/api/v1/banquet/events/"+str(e["id"])+":make-definite", map[string]any{"reason": "Club event"}).JSON(); d["event"].(map[string]any)["status"] != "definite" {
			t.Fatalf("open event definite: %v", d)
		}
		return str(e["id"])
	}
	// The server's clock: the gala's club day is read from the same instant
	// the API compares against.
	now := clock.Now().Truncate(time.Minute)
	galaStart := now.Add(2 * time.Hour)
	gala := mk("Charity Gala "+sfx, galaStart, 3, "100000")
	talk := mk("Golf Talk "+sfx, now.Add(72*time.Hour), 1, "0")

	// Website: Events page and Book Event with online payment.
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": map[string]any{"autoPay": false}})
	secret := str(pa.Must(200, "POST", "/api/v1/platform/integrations/"+mp+":rotate-webhook-secret", nil).JSON()["webhookSecret"])
	if list := pub.Must(200, "GET", "/api/v1/public/events?propertyId="+inst.Main.String(), nil).Items(); !containsID(list, gala) || !containsID(list, talk) {
		t.Fatalf("events page: %v", list)
	}
	pe := pub.Must(200, "GET", "/api/v1/public/events/"+gala+"?propertyId="+inst.Main.String(), nil).JSON()
	if pe["seatsLeft"].(float64) != 3 || pe["fee"] != "100000" {
		t.Fatalf("open event: %v", pe)
	}
	if page := pub.Must(200, "GET", "/api/v1/public/wedding-banquet?propertyId="+inst.Main.String(), nil).JSON(); page["packages"] == nil || page["venues"] == nil {
		t.Fatalf("wedding & banquet page: %v", page)
	}
	pub.Must(422, "POST", "/api/v1/public/events/"+gala+"/registrations", map[string]any{"propertyId": inst.Main, "guest": map[string]any{"name": "No contact"}})
	tk := pub.Must(201, "POST", "/api/v1/public/events/"+gala+"/registrations", map[string]any{"propertyId": inst.Main, "partySize": 2, "payMethod": "qris",
		"guest": map[string]any{"name": "Guest Web " + sfx, "phone": "+62825" + sfx, "email": "web" + sfx + "@guest.test"}}).JSON()
	co := tk["checkout"].(map[string]any)
	if tk["status"] != "registered" || tk["paymentStatus"] != "unpaid" || co["online"] == nil {
		t.Fatalf("book event: %v", tk)
	}
	bqEq(t, "fee for 2 seats", co["amountDue"], "200000")
	body, _ := json.Marshal(map[string]any{"id": "evt_" + uuid.NewString(), "type": "payment.paid",
		"data": map[string]any{"externalId": co["online"].(map[string]any)["externalId"], "status": "paid"}})
	pub.Must(200, "POST", "/api/v1/webhooks/mock-payment", body, integration.SignatureHeader, integration.Sign(secret, body, time.Now()))
	bqDispatch(t, "registration paid", func() bool {
		return pub.Must(200, "GET", "/api/v1/public/event-tickets/"+str(tk["ticketCode"]), nil).JSON()["paymentStatus"] == "paid"
	})
	pub.Must(404, "GET", "/api/v1/public/event-tickets/NOPE", nil)

	// Member App: list with my ticket, register, My Events.
	if evs := mc.Must(200, "GET", "/api/v1/member/events", nil).Items(); !containsID(evs, gala) {
		t.Fatalf("member events: %v", evs)
	}
	mt := mc.Must(201, "POST", "/api/v1/member/events/"+gala+"/registrations", map[string]any{"partySize": 1, "dietaryNotes": "Vegetarian"}).JSON()
	if mt["status"] != "registered" || mt["ticketCode"] == "" {
		t.Fatalf("member ticket: %v", mt)
	}
	mc.Must(409, "POST", "/api/v1/member/events/"+gala+"/registrations", map[string]any{})
	if x := mc.Must(200, "GET", "/api/v1/member/events/"+gala, nil).JSON(); x["myTicket"] == nil || x["seatsLeft"].(float64) != 0 {
		t.Fatalf("member event with my ticket: %v", x)
	}
	// Full: staff registration is waitlisted.
	wl := sa.Must(201, "POST", "/api/v1/banquet/events/"+gala+"/participants", map[string]any{"name": "Late Guest " + sfx, "email": "late" + sfx + "@guest.test"}).JSON()
	if wl["status"] != "waitlisted" || wl["waitlistRank"].(float64) != 1 {
		t.Fatalf("waitlisted: %v", wl)
	}
	// Free event: the member withdraws and the waitlist moves up.
	mtalk := mc.Must(201, "POST", "/api/v1/member/events/"+talk+"/registrations", map[string]any{}).JSON()
	wt := sa.Must(201, "POST", "/api/v1/banquet/events/"+talk+"/participants", map[string]any{"name": "Waiting " + sfx, "phone": "+62826" + sfx}).JSON()
	if wt["status"] != "waitlisted" {
		t.Fatalf("talk waitlist: %v", wt)
	}
	if w := mc.Must(200, "POST", "/api/v1/member/event-registrations/"+str(mtalk["ticketCode"])+":withdraw", map[string]any{"reason": "Travelling"}).JSON(); w["status"] != "withdrawn" {
		t.Fatalf("withdrawn: %v", w)
	}
	if p := sa.Must(200, "GET", "/api/v1/banquet/events/"+talk+"/participants?q="+str(wt["ticketCode"]), nil).Items(); len(p) != 1 || p[0]["status"] != "registered" {
		t.Fatalf("promoted from the waitlist: %v", p)
	}
	my := mc.Must(200, "GET", "/api/v1/member/my-events", nil).JSON()
	if len(my["tickets"].([]any)) < 2 {
		t.Fatalf("my events: %v", my)
	}
	// Guest list import (a duplicate is skipped).
	imp := sa.Must(200, "POST", "/api/v1/banquet/events/"+talk+"/participants:import", map[string]any{"participants": []map[string]any{
		{"name": "Imported One " + sfx, "email": "imp1" + sfx + "@guest.test"}, {"name": "Dup", "email": "imp1" + sfx + "@guest.test"}}}).JSON()
	if imp["waitlisted"].(float64) != 1 || len(imp["skipped"].([]any)) != 1 {
		t.Fatalf("import: %v", imp)
	}
	wd := sa.Must(200, "POST", "/api/v1/banquet/participants/"+str(imp["items"].([]any)[0].(map[string]any)["id"])+":withdraw", map[string]any{"reason": "Duplicate"}).JSON()

	// Event day: Today's Events, QR check-in, repeat scan, check-in from the list.
	// The gala starts in 2 hours, which is the next club day from 22:00 WIB
	// (15:00 UTC): Today's Events is opened on the gala's club day.
	galaDay := galaStart.In(clubLoc(inst)).Format("2006-01-02")
	if today := staff.Must(200, "GET", "/api/v1/banquet/today?date="+galaDay, nil).Items(); !strings.Contains(fmt.Sprint(today), gala) {
		t.Fatalf("today's events: %v", today)
	}
	ci := staff.Must(200, "POST", "/api/v1/banquet/events/"+gala+":check-in", map[string]any{"code": strings.ToLower(str(tk["ticketCode"]))}).JSON()
	if ci["participant"].(map[string]any)["status"] != "checked_in" || ci["alreadyCheckedIn"] != false {
		t.Fatalf("check-in: %v", ci)
	}
	if again := staff.Must(200, "POST", "/api/v1/banquet/events/"+gala+":check-in", map[string]any{"code": tk["ticketCode"]}).JSON(); again["alreadyCheckedIn"] != true {
		t.Fatalf("repeat scan: %v", again)
	}
	staff.Must(404, "POST", "/api/v1/banquet/events/"+gala+":check-in", map[string]any{"code": "UNKNOWN"})
	var memberPID string
	for _, p := range staff.Must(200, "GET", "/api/v1/banquet/events/"+gala+"/participants", nil).Items() {
		if p["ticketCode"] == mt["ticketCode"] {
			memberPID = str(p["id"])
		}
	}
	staff.Must(200, "POST", "/api/v1/banquet/participants/"+memberPID+":check-in", map[string]any{})
	// The member's check-in is published for CRM activity points.
	var checkedIn int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'banquet.event_guest_checked_in' AND aggregate_id = $1
		AND payload->>'customerId' IS NOT NULL AND payload->>'eventId' = $2`, []any{mustUUID(memberPID), gala}, &checkedIn)
	if checkedIn != 1 {
		t.Fatalf("banquet.event_guest_checked_in of the member: %d", checkedIn)
	}
	staff.Must(201, "POST", "/api/v1/banquet/events/"+gala+"/incidents", map[string]any{"note": "Projector replaced"})
	// Offline queue: the waitlisted guest promoted after a withdrawal is checked in once; a withdrawn ticket conflicts.
	sa.Must(200, "POST", "/api/v1/banquet/participants/"+str(wl["id"])+":withdraw", map[string]any{"reason": "No seat"})
	staffSA := sa
	reg := staffSA.Must(201, "POST", "/api/v1/banquet/events/"+gala+"/participants", map[string]any{"name": "Door Guest " + sfx}).JSON()
	if reg["status"] != "waitlisted" {
		// the gala is still full (3 seats checked in): door guests wait
		t.Fatalf("door guest: %v", reg)
	}
	items := []map[string]any{
		{"id": uuid.NewString(), "action": "banquet.event_check_in", "payload": map[string]any{"eventId": talk, "code": wt["ticketCode"]}},
		{"id": uuid.NewString(), "action": "banquet.event_check_in", "payload": map[string]any{"eventId": talk, "code": wd["ticketCode"]}},
	}
	// The talk starts in 3 days: check-in is not open yet → move it to now.
	sysExec(t, inst, `UPDATE banquet.events SET start_at = now() + interval '1 hour', end_at = now() + interval '4 hours' WHERE id = $1`, mustUUID(talk))
	res := staff.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": items}).JSON()["results"].([]any)
	if res[0].(map[string]any)["status"] != "accepted" || res[1].(map[string]any)["status"] != "conflict" {
		t.Fatalf("offline check-in: %v", res)
	}
	res = staff.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": items[:1]}).JSON()["results"].([]any)
	if res[0].(map[string]any)["status"] != "duplicate" {
		t.Fatalf("resent check-in: %v", res)
	}
	var checked int
	sysQueryRow(t, inst, `SELECT count(*) FROM banquet.participants WHERE event_id = $1 AND status = 'checked_in'`, []any{mustUUID(talk)}, &checked)
	if checked != 1 {
		t.Fatalf("checked in once: %d", checked)
	}
	_ = me
}

// FR-MIG-P3-01 / FR-MIG-P3-05: future banquets from the banquet book with
// the DP already received — dry run first, then the import (idempotent per
// reference): the DP becomes a held deposit on the event folio and the event
// is Definite; the reconciliation shows total DP = deposit liability and the
// number of future events.
func TestP3BanquetImport(t *testing.T) {
	sa := superAdmin(t, inst)
	fin := roleUser(t, inst, "finance_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	hall := bqVenue(t, sa, map[string]any{"code": "IM" + sfx, "name": "Import Hall " + sfx, "venueType": "ballroom", "maxCapacity": 300})
	pkg := idOf(sa.Must(201, "POST", "/api/v1/banquet/packages", map[string]any{"code": "IMP" + sfx, "name": "Import Wedding " + sfx, "category": "wedding",
		"pricingMethod": "fixed", "price": "50000000", "pricingMode": "nett", "includedPax": 200}))
	_ = pkg
	rows := []map[string]any{
		{"legacyRef": "BB-" + sfx + "-1", "title": "Wedding Lina & Bayu", "customerName": "Lina " + sfx, "customerPhone": "+62827" + sfx,
			"date": bqDay(45, 0, 0).Format("2006-01-02"), "startTime": "18:00", "endTime": "22:00", "pax": 250, "packageCode": "IMP" + sfx,
			"venueCode": "IM" + sfx, "contractTotal": "75000000", "downPayment": "22500000", "downPaymentDate": time.Now().AddDate(0, -2, 0).Format("2006-01-02"),
			"menu": "Menu A, extra dessert"},
		{"legacyRef": "BB-" + sfx + "-2", "title": "Arisan dinner", "eventType": "SOCIAL", "customerName": "Ibu Sri " + sfx, "customerEmail": "sri" + sfx + "@bq.test",
			"date": bqDay(20, 0, 0).Format("2006-01-02"), "pax": 40, "contractTotal": "8000000"},
		{"legacyRef": "BB-" + sfx + "-3", "title": "Past event", "customerName": "Old " + sfx, "date": "2020-01-01", "pax": 10, "contractTotal": "1000000"},
	}
	dry := fin.Must(200, "POST", "/api/v1/banquet/events:import", map[string]any{"dryRun": true, "rows": rows}).JSON()
	if dry["imported"].(float64) != 2 || dry["errors"].(float64) != 1 {
		t.Fatalf("dry run: %v", dry)
	}
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM banquet.events WHERE legacy_ref LIKE $1`, []any{"BB-" + sfx + "-%"}, &n)
	if n != 0 {
		t.Fatalf("dry run saved %d events", n)
	}
	res := fin.Must(200, "POST", "/api/v1/banquet/events:import", map[string]any{"rows": rows}).JSON()
	if res["imported"].(float64) != 2 {
		t.Fatalf("import: %v", res)
	}
	first := res["rows"].([]any)[0].(map[string]any)
	if first["eventStatus"] != "definite" || first["balance"] != "52500000" {
		t.Fatalf("imported wedding: %v", first)
	}
	again := fin.Must(200, "POST", "/api/v1/banquet/events:import", map[string]any{"rows": rows[:2]}).JSON()
	if again["existing"].(float64) != 2 || again["imported"].(float64) != 0 {
		t.Fatalf("re-import: %v", again)
	}
	ev := sa.Must(200, "GET", "/api/v1/banquet/events/"+str(first["eventId"]), nil).JSON()
	if bqHold(ev, str(hall["id"]))["status"] != "definite" || ev["source"] != "import" {
		t.Fatalf("imported event: %v", ev)
	}
	bqEq(t, "DP received", ev["billing"].(map[string]any)["received"], "22500000")
	rec := fin.Must(200, "GET", "/api/v1/banquet/migration/reconciliation", nil).JSON()
	if rec["balanced"] != true || rec["importedFutureEvents"].(float64) < 2 || bilDec(rec["migratedDownPayment"]).LessThan(decimal.NewFromInt(22_500_000)) {
		t.Fatalf("reconciliation: %v", rec)
	}
	bqEq(t, "deposit liability = migrated DP", rec["depositLiability"], str(rec["migratedDownPayment"]))
}

// FR-BQT-13 / §9.2 corporate event: a two-day MICE package per pax per day
// for the company, the requirement computed before any BEO, the final
// invoice to the corporate account (paid later: the event is settled by
// billing.invoice_paid), the banquet night audit check, and registrations
// paid at the desk or at the door.
func TestP3BanquetCorporate(t *testing.T) {
	sa := superAdmin(t, inst)
	fin := roleUser(t, inst, "finance_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	taxes := bqTax(t, sa, sfx)
	typ := bqType(t, sa, "MC"+sfx, "mice")
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "BQK" + sfx, "name": "PT Konferensi " + sfx,
		"email": "fin" + sfx + "@corp.test"}))
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "BQP" + sfx, "name": "PIC Konferensi " + sfx, "phone": "+62828" + sfx}))
	room := bqVenue(t, sa, map[string]any{"code": "JD" + sfx, "name": "Jade Room " + sfx, "venueType": "function_room", "maxCapacity": 80})
	mice := idOf(sa.Must(201, "POST", "/api/v1/banquet/packages", map[string]any{"code": "MFD" + sfx, "name": "Full Day Meeting " + sfx, "category": "mice",
		"pricingMethod": "per_pax_per_day", "price": "450000", "pricingMode": "plus_plus", "taxCodes": taxes, "minPax": 20, "durationHours": 8}))
	start := bqDay(40, 9, 0)
	ev := sa.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": "Annual Conference " + sfx, "eventTypeId": typ, "customerId": cust,
		"corporateAccountId": corp, "start": rfc(start), "end": rfc(start.Add(32 * time.Hour)), "expectedPax": 25, "packageId": mice,
		"venues": []map[string]any{{"venueId": room["id"], "functionName": "Plenary", "layout": ""}}}).JSON()
	eid := str(ev["id"])
	bqEq(t, "25 pax × 2 days ++", ev["contractTotal"], "25987500")
	if pr := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid+"/procurement-requirement", nil).JSON(); pr["beoId"] != nil || pr["eventNumber"] != ev["number"] {
		t.Fatalf("computed requirement: %v", pr)
	}
	bl := sa.Must(201, "POST", "/api/v1/banquet/events/"+eid+"/payment-schedule", map[string]any{}).JSON()
	sc := bl["schedule"].(map[string]any)
	sa.Must(201, "POST", "/api/v1/billing/payment-schedules/"+str(sc["id"])+"/lines/"+str(sc["lines"].([]any)[0].(map[string]any)["id"])+":pay",
		map[string]any{"methodType": "bank_transfer", "reference": "TRF-" + sfx})
	bqDispatch(t, "corporate event definite", func() bool {
		return sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()["status"] == "definite"
	})
	sa.Must(200, "POST", "/api/v1/banquet/events/"+eid+":complete", map[string]any{"finalPax": 25})
	// Night audit: the completed event waits for its final billing.
	var findings []map[string]any
	ctx := dbtx.System(t.Context())
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		f, err := inst.App.Banquet.Module.NightAuditCheck(ctx, tx, inst.Main, start.AddDate(0, 0, 2))
		for _, x := range f {
			findings = append(findings, map[string]any{"check": x.Check, "count": x.Count, "items": x.Items})
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 || !strings.Contains(fmt.Sprint(findings[1]["items"]), str(ev["number"])) {
		t.Fatalf("night audit findings: %v", findings)
	}
	fb := fin.Must(200, "POST", "/api/v1/banquet/events/"+eid+":final-billing", map[string]any{}).JSON()
	inv := fb["billing"].(map[string]any)["finalInvoice"].(map[string]any)
	if fb["folioClosed"] != true || inv["status"] != "issued" || str(inv["corporateAccountId"]) != corp {
		t.Fatalf("final invoice to the company: %v", fb)
	}
	bqEq(t, "invoice = contract − DP", inv["total"], decimal.RequireFromString("25987500").Sub(decimal.RequireFromString(str(sc["lines"].([]any)[0].(map[string]any)["amount"]))).String())
	if e := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON(); e["settledAt"] != nil || e["finalInvoiceId"] == nil {
		t.Fatalf("invoiced, not settled yet: %v", e)
	}
	fin.Must(200, "POST", "/api/v1/billing/invoices/"+str(inv["id"])+":pay", map[string]any{"methodType": "bank_transfer", "reference": "TRF-INV-" + sfx})
	bqDispatch(t, "settled by the paid invoice", func() bool { return sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()["settledAt"] != nil })

	// Registrations paid at the desk and at the door.
	host := idOf(sa.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": "Wine Dinner " + sfx, "eventTypeId": typ, "customerId": cust,
		"start": rfc(time.Now().Add(time.Hour)), "end": rfc(time.Now().Add(4 * time.Hour)), "expectedPax": 20, "public": true, "registrationOpen": true,
		"capacity": 20, "registrationFee": "750000"}))
	sa.Must(200, "POST", "/api/v1/banquet/events/"+host+":make-definite", map[string]any{})
	paid := sa.Must(201, "POST", "/api/v1/banquet/events/"+host+"/participants", map[string]any{"name": "Desk Guest " + sfx, "partySize": 2,
		"payment": map[string]any{"methodType": "cash"}}).JSON()
	if paid["paymentStatus"] != "paid" || paid["fee"] != "1500000" {
		t.Fatalf("paid at the desk: %v", paid)
	}
	door := sa.Must(201, "POST", "/api/v1/banquet/events/"+host+"/participants", map[string]any{"name": "Door Guest " + sfx}).JSON()
	ci := sa.Must(200, "POST", "/api/v1/banquet/participants/"+str(door["id"])+":check-in", map[string]any{"payment": map[string]any{"methodType": "cash"}}).JSON()
	if p := ci["participant"].(map[string]any); p["status"] != "checked_in" || p["paymentStatus"] != "paid" {
		t.Fatalf("paid at the door: %v", ci)
	}
}

var _ = json.Marshal
var _ = uuid.Nil
var _ = integration.SignatureHeader
