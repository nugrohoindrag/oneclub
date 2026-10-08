package e2e

import (
	"testing"
	"time"
)

// POS Table View (FR-POS-01 dine-in, POS design 7 Oct 2026): tables on the
// floor plan; a reservation shows its table Booked; an order seats tables
// (Occupied, a second order on them is refused); Print Bill shows Billed
// until items are added; payment frees the table; the cart quote equals the
// priced order.
func TestPOSTableView(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "TV-RESTO", "name": "Table View Restaurant",
		"outletType": "restaurant", "pricingMode": "plus_plus"}))
	soup := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "TV-SOTO", "name": "Soto Ayam", "productType": "food",
		"price": "65000", "imageUrl": "https://example.org/soto.webp"}))
	sa.Must(422, "PATCH", "/api/v1/commercial/products/"+soup, map[string]any{"imageUrl": "javascript:alert(1)"})
	photo := ""
	for _, it := range sa.Must(200, "GET", "/api/v1/commercial/outlets/"+outlet+"/menu", nil).Items() {
		if it["code"] == "TV-SOTO" {
			photo = str(it["imageUrl"])
		}
	}
	if photo != "https://example.org/soto.webp" {
		t.Fatalf("menu photo: %q", photo)
	}
	t1 := idOf(sa.Must(201, "POST", "/api/v1/commercial/dining-tables", map[string]any{"code": "T-1", "outletId": outlet, "seats": 4, "posX": "20", "posY": "30"}))
	t2 := idOf(sa.Must(201, "POST", "/api/v1/commercial/dining-tables", map[string]any{"code": "T-2", "outletId": outlet, "shape": "round", "seats": 2}))
	other := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "TV-OTHER", "name": "Other"}))
	sa.Must(201, "POST", "/api/v1/commercial/dining-tables", map[string]any{"code": "T-1", "outletId": other}) // codes repeat per outlet
	state := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, x := range sa.Must(200, "GET", "/api/v1/commercial/outlets/"+outlet+"/tables", nil).Items() {
			out[str(x["code"])] = x
		}
		return out
	}
	res := sa.Must(201, "POST", "/api/v1/commercial/table-reservations", map[string]any{"outletId": outlet, "tableIds": []string{t2}, "guestName": "Budi",
		"guestCount": 2, "reservedFor": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}).JSON()
	if res["status"] != "booked" {
		t.Fatalf("reservation: %v", res)
	}
	sa.Must(422, "POST", "/api/v1/commercial/table-reservations", map[string]any{"outletId": outlet, "tableIds": []string{t1}, "guestCount": 2,
		"reservedFor": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}) // no guest name nor customer
	if s := state(); s["T-1"]["state"] != "available" || s["T-2"]["state"] != "booked" {
		t.Fatalf("states: %v / %v", s["T-1"]["state"], s["T-2"]["state"])
	}

	// the cart quote is what the order charges
	quote := sa.Must(200, "POST", "/api/v1/commercial/outlets/"+outlet+":quote", map[string]any{"amount": "130000"}).JSON()
	o := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "tableIds": []string{t1}, "guestCount": 3, "send": true,
		"lines": []map[string]any{{"productId": soup, "quantity": "2"}}}).JSON()
	oid := str(o["id"])
	if o["tableNo"] != "T-1" || o["total"] != quote["total"] {
		t.Fatalf("seated order: tableNo %v total %v, quote %v", o["tableNo"], o["total"], quote["total"])
	}
	if s := state(); s["T-1"]["state"] != "occupied" || s["T-1"]["order"].(map[string]any)["orderNo"] != o["orderNo"] {
		t.Fatalf("occupied: %v", s["T-1"])
	}
	if r := sa.Do("POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "tableIds": []string{t1},
		"lines": []map[string]any{{"productId": soup}}}); r.Status != 409 || r.JSON()["code"] != "table_occupied" {
		t.Fatalf("second order on T-1: %d %s", r.Status, r.Body)
	}
	sa.Must(200, "POST", "/api/v1/commercial/orders/"+oid+":bill", nil)
	if s := state(); s["T-1"]["state"] != "billed" {
		t.Fatalf("billed: %v", s["T-1"]["state"])
	}
	sa.Must(200, "POST", "/api/v1/commercial/orders/"+oid+"/lines", map[string]any{"lines": []map[string]any{{"productId": soup}}})
	if s := state(); s["T-1"]["state"] != "occupied" {
		t.Fatalf("new items after the bill: %v", s["T-1"]["state"])
	}

	// the reservation is seated by its order
	seated := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "tableIds": []string{t2}, "tableReservationId": res["id"],
		"lines": []map[string]any{{"productId": soup}}}).JSON()
	if r := sa.Must(200, "GET", "/api/v1/commercial/table-reservations/"+str(res["id"]), nil).JSON(); r["status"] != "seated" || r["orderId"] != seated["id"] {
		t.Fatalf("reservation after seating: %v", r)
	}

	// Move Table: not onto a seated table; to a free one (T-1 is released)
	t3 := idOf(sa.Must(201, "POST", "/api/v1/commercial/dining-tables", map[string]any{"code": "T-3", "outletId": outlet}))
	if r := sa.Do("POST", "/api/v1/commercial/orders/"+oid+":tables", map[string]any{"tableIds": []string{t2}}); r.Status != 409 {
		t.Fatalf("move onto a seated table: %d %s", r.Status, r.Body)
	}
	if m := sa.Must(200, "POST", "/api/v1/commercial/orders/"+oid+":tables", map[string]any{"tableIds": []string{t3}}).JSON(); m["tableNo"] != "T-3" {
		t.Fatalf("moved: %v", m["tableNo"])
	}
	if s := state(); s["T-1"]["state"] != "available" || s["T-3"]["state"] != "occupied" || s["T-3"]["order"].(map[string]any)["serviceStatus"] != "sent" {
		t.Fatalf("after move: %v / %v", s["T-1"], s["T-3"])
	}

	// payment frees the table
	shift := sa.Must(201, "POST", "/api/v1/commercial/shifts:open", map[string]any{"outletId": outlet, "openingCash": "0"}).JSON()
	sa.Must(200, "POST", "/api/v1/commercial/orders/"+oid+":pay", map[string]any{"shiftId": shift["id"], "tenders": []map[string]any{{"methodType": "cash"}}})
	if s := state(); s["T-3"]["state"] != "available" || s["T-2"]["state"] != "occupied" {
		t.Fatalf("after payment: %v / %v", s["T-3"]["state"], s["T-2"]["state"])
	}

	// offline: a dine-in order, items added to it and its payment replay from the sync queue in order
	offline := "01a20000-0000-7000-8000-000000000001"
	items := []map[string]any{
		{"id": "01a20000-0000-7000-8000-0000000000a1", "action": "commercial.pos_order", "clientTime": time.Now().UTC().Format(time.RFC3339),
			"payload": map[string]any{"order": map[string]any{"id": offline, "outletId": outlet, "tableIds": []string{t1}, "send": true,
				"lines": []map[string]any{{"productId": soup}}}}},
		{"id": "01a20000-0000-7000-8000-0000000000a2", "action": "commercial.pos_order", "clientTime": time.Now().UTC().Format(time.RFC3339),
			"payload": map[string]any{"orderId": offline, "lines": []map[string]any{{"productId": soup, "quantity": "2"}}}},
		{"id": "01a20000-0000-7000-8000-0000000000a3", "action": "commercial.pos_order", "clientTime": time.Now().UTC().Format(time.RFC3339),
			"payload": map[string]any{"orderId": offline, "payment": map[string]any{"shiftId": shift["id"], "tenders": []map[string]any{{"methodType": "cash"}}}}},
	}
	for _, r := range sa.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": items}).JSON()["results"].([]any) {
		if r.(map[string]any)["status"] != "accepted" {
			t.Fatalf("sync: %v", r)
		}
	}
	paid := sa.Must(200, "GET", "/api/v1/commercial/orders/"+offline, nil).JSON()
	if paid["status"] != "paid" || len(paid["lines"].([]any)) != 2 || !paid["offline"].(bool) {
		t.Fatalf("offline order: %v %v", paid["status"], paid["lines"])
	}
	if s := state(); s["T-1"]["state"] != "available" {
		t.Fatalf("after the offline sale: %v", s["T-1"]["state"])
	}
}
