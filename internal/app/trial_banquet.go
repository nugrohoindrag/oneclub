package app

// Trial dataset: banquet & events. At go-live the banquet menu and the
// packages (wedding, corporate meeting, social gathering) are set up. Two
// events a week run through the whole cycle: inquiry three weeks before
// (tentative, venue held), payment schedule and down payment (definite),
// guaranteed pax and menu selection a week before, the BEO issued to the
// departments, the event completed with the final pax and the final
// billing (company events invoiced to the corporate account, AR). Events
// of the coming weeks are booked at the end (tentative and definite).

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "banquet", Order: 50, Setup: trialBanquetSetup, Day: trialBanquetDay, Final: trialBanquetFinal})
}

const (
	trialBanquetSales   = "banquet.sales@demo.oneclub.id"
	trialBanquetManager = "banquet.manager@demo.oneclub.id"
)

// trialEvent is one planned event of the trial.
type trialEvent struct {
	Key, Title, Type, Package, Venue, Layout string
	Day                                      time.Time
	Start, Hours                             int
	Pax, FinalPax                            int
	Corporate                                bool
	Guest                                    int // index of the customer
}

// trialEvents plans the events: a wedding or social gathering on
// Saturdays, a corporate meeting on Wednesdays, from three weeks before the
// history to the end of the upcoming window.
func (t *Trial) trialEvents() []trialEvent {
	var out []trialEvent
	r := t.Rand("events")
	for d := t.Start; d.Before(t.Today.AddDate(0, 0, t.Ahead)); d = d.AddDate(0, 0, 1) {
		switch d.Weekday() {
		case time.Saturday:
			if r.IntN(3) > 0 {
				pax := 250 + 10*r.IntN(15)
				out = append(out, trialEvent{Key: d.Format("20060102") + "-W", Title: "Wedding " + trialMaleNames[r.IntN(len(trialMaleNames))] + " & " +
					trialFemaleNames[r.IntN(len(trialFemaleNames))], Type: "WEDDING", Package: "TRL-WEDDING", Venue: "BALLROOM", Layout: "round_table",
					Day: d, Start: 11, Hours: 5, Pax: pax, FinalPax: pax - 10 + r.IntN(25), Guest: r.IntN(1000)})
			} else {
				pax := 60 + 5*r.IntN(10)
				out = append(out, trialEvent{Key: d.Format("20060102") + "-S", Title: "Family Gathering " + trialLastNames[r.IntN(len(trialLastNames))],
					Type: "FAMILY_GATHERING", Package: "TRL-SOCIAL", Venue: "BALLROOM-A", Layout: "round_table", Day: d, Start: 18, Hours: 4, Pax: pax,
					FinalPax: pax - 3 + r.IntN(8), Guest: r.IntN(1000)})
			}
		case time.Wednesday:
			pax := 25 + 5*r.IntN(5)
			out = append(out, trialEvent{Key: d.Format("20060102") + "-M", Title: "Rapat Kerja " + trialCorporates[r.IntN(len(trialCorporates))][0],
				Type: "MEETING", Package: "TRL-MICE", Venue: "SAPPHIRE", Layout: "classroom", Day: d, Start: 8, Hours: 9, Pax: pax,
				FinalPax: pax - 2 + r.IntN(5), Corporate: true, Guest: r.IntN(len(trialCorporates))})
		}
	}
	return out
}

func trialBanquetSetup(ctx context.Context, t *Trial) error {
	bm := t.As(trialBanquetManager)
	menu := bm.Post("/api/v1/banquet/menus", J{"code": "TRL-BUFFET", "name": "Buffet Nusantara", "menuType": "buffet", "pricePerPax": "225000",
		"pricingMode": "plus_plus", "taxCodes": []string{"SVC", "PB1"}}).S("id")
	for _, c := range []struct {
		name  string
		quota int
		items []string
	}{{"Appetizer", 2, []string{"Gado-gado", "Lumpia Udang", "Caesar Salad"}}, {"Main Course", 3, []string{"Rendang Sapi", "Ayam Bakar Taliwang",
		"Ikan Bakar Jimbaran", "Sapo Tahu"}}, {"Dessert", 1, []string{"Es Campur", "Puding Coklat"}}} {
		cat := bm.Post("/api/v1/banquet/menu-categories", J{"menuId": menu, "name": c.name, "quota": c.quota, "extraChoicePrice": "25000"}).S("id")
		for _, it := range c.items {
			bm.Post("/api/v1/banquet/menu-items", J{"menuId": menu, "categoryId": cat, "name": it, "station": "buffet", "serveOffsetMinutes": 60})
		}
	}
	for _, p := range []J{
		{"code": "TRL-WEDDING", "name": "Wedding Gold Package", "category": "wedding", "pricingMethod": "fixed", "price": "95000000", "pricingMode": "nett",
			"includedPax": 300, "extraPaxPrice": "225000", "extraPaxMode": "plus_plus", "durationHours": 5, "menuId": menu, "public": true,
			"taxCodes": []string{"SVC", "PB1"}, "inclusions": []J{{"kind": "service", "label": "Food tasting for 6"}, {"kind": "service", "label": "Bridal room"}}},
		{"code": "TRL-MICE", "name": "Full Day Meeting Package", "category": "mice", "pricingMethod": "per_pax", "price": "385000", "pricingMode": "plus_plus",
			"minPax": 20, "durationHours": 9, "taxCodes": []string{"SVC", "PB1"}, "inclusions": []J{{"kind": "service", "label": "2 coffee breaks and lunch"}}},
		{"code": "TRL-SOCIAL", "name": "Social Gathering Package", "category": "social", "pricingMethod": "per_pax", "price": "295000", "pricingMode": "plus_plus",
			"minPax": 40, "durationHours": 4, "menuId": menu, "taxCodes": []string{"SVC", "PB1"}},
	} {
		bm.Post("/api/v1/banquet/packages", p)
	}
	// events whose inquiry came before the go-live
	for _, e := range t.trialEvents() {
		if !e.Day.AddDate(0, 0, -21).Before(t.Start) {
			continue
		}
		trialEventInquiry(t, e)
		if e.Day.AddDate(0, 0, -7).Before(t.Start) {
			trialEventFinalize(t, e)
		}
	}
	return nil
}

// trialEventByKey finds the event of a plan (by its key in the notes).
func trialEventByKey(t *Trial, key string) J {
	if id := t.Ref("event:" + key); id != "" {
		return t.As(trialBanquetSales).Get("/api/v1/banquet/events/" + id)
	}
	for _, e := range t.As(trialBanquetSales).Items("/api/v1/banquet/events?limit=500") {
		if strings.Contains(e.S("notes"), "["+key+"]") {
			t.SetRef("event:"+key, e.S("id"))
			return t.As(trialBanquetSales).Get("/api/v1/banquet/events/" + e.S("id"))
		}
	}
	return nil
}

// trialEventInquiry books an event (tentative), issues its payment
// schedule and takes the down payment (definite).
func trialEventInquiry(t *Trial, e trialEvent) {
	bs := t.As(trialBanquetSales)
	typ := t.ids("eventtype", "/api/v1/banquet/event-types?limit=50", trialBanquetSales)
	venue := t.ids("venue", "/api/v1/banquet/venues?limit=50", trialBanquetSales)
	pkg := t.ids("bqpackage", "/api/v1/banquet/packages?limit=50", trialBanquetSales)
	start := t.Clock(e.Day, fmt.Sprintf("%02d:00", e.Start))
	body := J{"title": e.Title, "eventTypeId": typ(e.Type), "start": start.Format(time.RFC3339), "end": start.Add(time.Duration(e.Hours) * time.Hour).Format(time.RFC3339),
		"expectedPax": e.Pax, "layout": e.Layout, "packageId": pkg(e.Package), "notes": "Trial dataset [" + e.Key + "]",
		"venues": []J{{"venueId": venue(e.Venue), "functionName": map[bool]string{true: "Meeting", false: "Reception"}[e.Corporate]}}}
	if e.Corporate {
		corp := t.ids("corporate", "/api/v1/crm/corporate-accounts?limit=50", trialBanquetSales)
		body["corporateAccountId"] = corp(fmt.Sprintf("RHC-K%02d", e.Guest+1))
		body["contactName"], body["contactEmail"] = "HR & GA Manager", fmt.Sprintf("hrga%d@corp.trial.test", e.Guest+1)
	} else {
		g := t.Guests()[e.Guest%len(t.Guests())]
		body["customerId"] = g.ID
	}
	ev := bs.Post("/api/v1/banquet/events", body)
	t.SetRef("event:"+e.Key, ev.S("id"))
	bl := bs.Post("/api/v1/banquet/events/"+ev.S("id")+"/payment-schedule", J{})
	sch := bl.M("schedule")
	if lines := sch.A("lines"); len(lines) > 0 {
		t.As(trialCashier).Post("/api/v1/billing/payment-schedules/"+sch.S("id")+"/lines/"+lines[0].S("id")+":pay", J{"methodType": "bank_transfer",
			"reference": "TRF-DP-" + e.Key})
	}
}

func trialBanquetDay(ctx context.Context, t *Trial, day time.Time) error {
	for _, e := range t.trialEvents() {
		switch {
		case e.Day.AddDate(0, 0, -21).Equal(day):
			t.At(day, "10:00")
			trialEventInquiry(t, e)
		case e.Day.AddDate(0, 0, -7).Equal(day):
			t.At(day, "11:00")
			trialEventFinalize(t, e)
		case e.Day.Equal(day):
			t.At(day, fmt.Sprintf("%02d:00", e.Start+e.Hours))
			trialEventComplete(t, e)
		}
	}
	return nil
}

// trialEventFinalize: guaranteed pax, menu selection, BEO issued (H-7).
func trialEventFinalize(t *Trial, e trialEvent) {
	bs, bm := t.As(trialBanquetSales), t.As(trialBanquetManager)
	t.check(t.flush(context.Background())) // the down payment makes the event definite
	ev := trialEventByKey(t, e.Key)
	if ev == nil || ev.S("status") == "cancelled" {
		return
	}
	eid := ev.S("id")
	bs.Post("/api/v1/banquet/events/"+eid+":guarantee-pax", J{"pax": e.Pax, "reason": "Final guest list"})
	if e.Package != "TRL-MICE" {
		var menuID string
		var items []string
		for _, m := range bs.Items("/api/v1/banquet/menus?limit=50") {
			if m.S("code") == "TRL-BUFFET" {
				menuID = m.S("id")
			}
		}
		quota := map[string]int{}
		for _, c := range bs.Items("/api/v1/banquet/menu-categories?limit=50&filter[menuId]=" + menuID) {
			quota[c.S("id")] = int(c.N("quota"))
		}
		for _, it := range bs.Items("/api/v1/banquet/menu-items?limit=50&filter[menuId]=" + menuID) {
			if quota[it.S("categoryId")] > 0 {
				quota[it.S("categoryId")]--
				items = append(items, it.S("id"))
			}
		}
		bs.Put("/api/v1/banquet/events/"+eid+"/menu-selection", J{"menuId": menuID, "itemIds": items})
	}
	b := bs.Post("/api/v1/banquet/beos", J{"eventId": eid, "instructions": J{"kitchen": "Halal kitchen; buffet ready 30 minutes before", "venue": "Layout " + e.Layout,
		"engineering": "Sound system and LED screen"}, "notes": "Trial dataset BEO"})
	bm.Do("POST", "/api/v1/banquet/beos/"+b.S("id")+":issue", J{}, "If-Match", fmt.Sprintf(`"%v"`, b["rev"]))
}

// trialEventComplete closes the event day: completion with the final pax
// and the final billing (companies are invoiced).
func trialEventComplete(t *Trial, e trialEvent) {
	bs, bm := t.As(trialBanquetSales), t.As(trialBanquetManager)
	ev := trialEventByKey(t, e.Key)
	if ev == nil || ev.S("status") != "definite" {
		return
	}
	eid := ev.S("id")
	bm.Post("/api/v1/banquet/events/"+eid+":complete", J{"finalPax": e.FinalPax, "notes": "Event ran on time"})
	if e.Corporate {
		bs.Post("/api/v1/banquet/events/"+eid+":final-billing", J{"invoice": true, "termsDays": 30})
		return
	}
	bs.Post("/api/v1/banquet/events/"+eid+":final-billing", J{"payment": J{"methodType": "bank_transfer", "reference": "TRF-" + e.Key}})
}

// trialBanquetFinal books the events of the coming weeks (inquiries; the
// nearer ones with their down payment).
func trialBanquetFinal(_ context.Context, t *Trial) error {
	for _, e := range t.trialEvents() {
		if e.Day.Before(t.Today) || trialEventByKey(t, e.Key) != nil {
			continue
		}
		trialEventInquiry(t, e)
		if e.Day.Sub(t.Today) <= 7*24*time.Hour {
			trialEventFinalize(t, e)
		}
	}
	return nil
}
