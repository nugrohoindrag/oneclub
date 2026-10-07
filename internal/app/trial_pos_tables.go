package app

// Trial dataset: POS Table View. At go-live the outlets get their floor
// plans (dining tables and bar seats) and the products their photos
// (internal/app/trialphotos, open licenses). Today the Clubhouse Restaurant
// is in service: tables seated with open orders, two bills presented and
// reservations due in the coming hours.

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"oneclub/internal/app/trialphotos"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "pos-tables", Order: 31, Setup: trialPOSTablesSetup, Final: trialPOSTablesFinal})
}

type trialTable struct {
	code, area, shape string
	seats             int
	x, y              float64
}

// trialFloor is a floor plan: cols × rows tables of the dining room (shapes
// and seats repeat by column) and a column of bar seats on the right.
func trialFloor(cols, rows, bar int) []trialTable {
	shapes := []struct {
		shape string
		seats int
	}{{"round", 2}, {"square", 4}, {"rect", 6}, {"square", 4}}
	var out []trialTable
	for r := range rows {
		for c := range cols {
			s := shapes[(c+r)%len(shapes)]
			area := "Dining Room"
			if r == rows-1 && rows > 2 {
				area = "Terrace"
			}
			out = append(out, trialTable{code: fmt.Sprintf("T-%d", r*cols+c+1), area: area, shape: s.shape, seats: s.seats,
				x: 10 + float64(c)*72/float64(max(cols-1, 1)), y: 14 + float64(r)*72/float64(max(rows-1, 1))})
		}
	}
	for i := range bar {
		out = append(out, trialTable{code: fmt.Sprintf("B-%d", i+1), area: "Bar", shape: "seat", seats: 1, x: 94,
			y: 8 + float64(i)*84/float64(max(bar-1, 1))})
	}
	return out
}

// trialFloors are the floor plans per outlet (the Pro Shop sells over the counter).
var trialFloors = map[string]func() []trialTable{
	"RESTO":     func() []trialTable { return trialFloor(4, 4, 8) },
	"BAR":       func() []trialTable { return trialFloor(3, 2, 10) },
	"HALFWAY":   func() []trialTable { return trialFloor(3, 2, 4) },
	"SPORTCAFE": func() []trialTable { return trialFloor(4, 2, 0) },
}

func trialPOSTablesSetup(_ context.Context, t *Trial) error {
	om := t.As(trialOutletManager)
	outlet := t.ids("outlet", "/api/v1/commercial/outlets?limit=100", trialOutletManager)
	for code, floor := range trialFloors {
		for _, tb := range floor() {
			om.Post("/api/v1/commercial/dining-tables", J{"code": tb.code, "outletId": outlet(code), "area": tb.area, "shape": tb.shape,
				"seats": tb.seats, "posX": fmt.Sprintf("%.2f", tb.x), "posY": fmt.Sprintf("%.2f", tb.y)})
		}
	}
	product := t.ids("product", "/api/v1/commercial/products?limit=200", trialOutletManager)
	for _, p := range trialProducts {
		data, err := trialphotos.FS.ReadFile(p.code + ".webp")
		if err != nil {
			return fmt.Errorf("photo of %s: %w", p.code, err)
		}
		f := om.Upload("/api/v1/platform/images?resource=commercial.product", p.code+".webp", data)
		om.Patch("/api/v1/commercial/products/"+product(p.code), J{"imageUrl": f.S("url")})
	}
	return nil
}

// trialPOSTablesFinal puts the Clubhouse Restaurant in service now.
func trialPOSTablesFinal(_ context.Context, t *Trial) error {
	const resto = "RESTO"
	cashier := t.As("pos@demo.oneclub.id")
	outlet := t.ids("outlet", "/api/v1/commercial/outlets?limit=100", trialOutletManager)(resto)
	product := t.ids("product", "/api/v1/commercial/products?limit=200", trialOutletManager)
	tables, seated := map[string]string{}, map[string]bool{}
	for _, x := range cashier.Items("/api/v1/commercial/outlets/" + outlet + "/tables") {
		tables[x.S("code")] = x.S("id")
		seated[x.S("code")] = x.S("state") == "occupied" || x.S("state") == "billed" // a repeated step keeps its orders
	}
	members := t.Members()
	member := func(i int) any {
		if len(members) == 0 {
			return nil
		}
		return members[(i*37)%len(members)].CustomerID.String()
	}
	type seat struct {
		tables []string
		guests int
		lines  [][2]any // product code, quantity
		billed bool
	}
	for i, s := range []seat{
		{[]string{"T-3"}, 2, [][2]any{{"NASGOR", 2}, {"ESJERUK", 2}}, false},
		{[]string{"T-6"}, 5, [][2]any{{"SOPBUNTUT", 2}, {"BURGER", 2}, {"FISHCHIPS", 1}, {"TEH", 5}}, false},
		{[]string{"T-9", "T-10"}, 8, [][2]any{{"MIEGOR", 3}, {"SOTO", 3}, {"SANDWICH", 2}, {"COLA", 4}, {"LATTE", 4}}, true},
		{[]string{"T-12"}, 4, [][2]any{{"BURGER", 2}, {"FRIES", 2}, {"KOPI", 4}}, true},
		{[]string{"T-15"}, 3, [][2]any{{"NASGOR", 1}, {"SOTO", 2}, {"AIR-600", 3}}, false},
		{[]string{"B-2"}, 1, [][2]any{{"KOPI", 1}, {"FRIES", 1}}, false},
	} {
		if seated[s.tables[0]] {
			continue
		}
		var ids []string
		for _, c := range s.tables {
			ids = append(ids, tables[c])
		}
		var lines []J
		for _, l := range s.lines {
			lines = append(lines, J{"productId": product(l[0].(string)), "quantity": fmt.Sprint(l[1])})
		}
		body := J{"outletId": outlet, "tableIds": ids, "guestCount": s.guests, "orderType": "dine_in", "lines": lines, "send": true}
		if c := member(i); c != nil && i%2 == 0 {
			body["customerId"] = c
		}
		o := cashier.Post("/api/v1/commercial/orders", body)
		if s.billed {
			cashier.Post("/api/v1/commercial/orders/"+o.S("id")+":bill", nil)
		}
	}
	now := time.Now().In(t.Loc).Truncate(15 * time.Minute)
	for i, r := range []struct {
		tables []string
		in     time.Duration
		guests int
		name   string
	}{
		{[]string{"T-2"}, time.Hour, 4, ""},
		{[]string{"T-11"}, 2 * time.Hour, 6, ""},
		{[]string{"B-5", "B-6"}, 45 * time.Minute, 2, "Andi Wijaya"},
	} {
		var ids []string
		for _, c := range r.tables {
			ids = append(ids, tables[c])
		}
		body := J{"outletId": outlet, "tableIds": ids, "guestCount": r.guests, "reservedFor": now.Add(r.in).Format(time.RFC3339),
			"phone": "0812-555-01" + fmt.Sprint(10+i)}
		if c := member(10 + i); r.name == "" && c != nil {
			body["customerId"] = c
		} else {
			body["guestName"] = cmp.Or(r.name, "Walk-in Guest")
		}
		cashier.Post("/api/v1/commercial/table-reservations", body)
	}
	return nil
}
