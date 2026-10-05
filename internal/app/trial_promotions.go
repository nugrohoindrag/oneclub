package app

// Trial dataset: promotions and packages. At go-live the outlet manager
// activates the demo promotions (Happy Hour "Buy 2 Only 200K", the member
// F&B discount and the WELCOME15 code), so the POS sales of the history
// carry promotion redemptions, and the club publishes a "Family Day"
// package (pool entry + lunch buffet per pax). Mid-week the reservation
// team books the families of the coming weekend; on the day the components
// are consumed at the pool and the restaurant.

import (
	"context"
	"fmt"
	"time"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "promotions", Order: 35, Setup: trialPromotionsSetup, Day: trialPromotionsDay, Final: trialPromotionsFinal})
}

const trialReservation = "reservation@demo.oneclub.id"

func trialPromotionsSetup(ctx context.Context, t *Trial) error {
	om := t.As(trialOutletManager)
	promo := t.ids("promotion", "/api/v1/commercial/promotions?limit=100", trialOutletManager)
	for _, code := range []string{"HH-BUY2", "MEMBER10", "WELCOME15"} {
		if id := promo(code); id != "" {
			om.Post("/api/v1/commercial/promotions/"+id+":activate", nil)
		}
	}
	t.ApprovePending(ctx)
	admin := t.Admin()
	pkg := admin.Post("/api/v1/commercial/packages", J{"code": "TRL-FAMILY", "name": "Family Day", "packageType": "family", "pricingMode": "per_pax",
		"price": "275000", "minPax": 2, "maxPax": 12, "startTime": "10:00", "taxMode": "nett", "public": true, "validDays": []int{6, 7},
		"description": "Pool entry and the lunch buffet at the clubhouse restaurant"}).S("id")
	admin.Post("/api/v1/commercial/package-components", J{"packageId": pkg, "componentType": "service", "name": "Pool entry", "perPax": true,
		"quantity": "1", "standalonePrice": "100000", "revenueComponent": "sport_entry", "businessLine": "sportclub"})
	admin.Post("/api/v1/commercial/package-components", J{"packageId": pkg, "componentType": "fnb", "name": "Lunch buffet", "perPax": true,
		"quantity": "1", "standalonePrice": "175000", "revenueComponent": "fnb", "businessLine": "pos"})
	admin.Post("/api/v1/commercial/packages/"+pkg+":publish", nil)
	t.SetRef("package:TRL-FAMILY", pkg)
	return nil
}

// trialFamilyBookings books the Family Days of the weekend after day.
func trialFamilyBookings(t *Trial, day time.Time) {
	r := t.Rand("family:" + day.Format(time.DateOnly))
	rs := t.As(trialReservation)
	pkg := t.ids("package", "/api/v1/commercial/packages?limit=100", trialReservation)("TRL-FAMILY")
	sat := day
	for sat.Weekday() != time.Saturday {
		sat = sat.AddDate(0, 0, 1)
	}
	for i := range 1 + r.IntN(3) {
		start := sat.AddDate(0, 0, i%2)
		body := J{"packageId": pkg, "startDate": start.Format(time.DateOnly), "pax": 2 + r.IntN(5), "channel": "back_office",
			"payment": J{"methodType": []string{"card", "qris", "bank_transfer"}[r.IntN(3)], "reference": fmt.Sprintf("PKG-%06d", r.IntN(1000000))},
			"notes":   "Trial family day " + trialDayTag(start)}
		if ms := t.MembersOn(start); len(ms) > 0 && r.IntN(2) == 0 {
			body["customerId"] = ms[r.IntN(len(ms))].CustomerID.String()
		} else {
			p := trialPersonOf(r, 60000+r.IntN(5000), "F")
			body["guestName"], body["guestPhone"], body["guestEmail"] = p.Name, p.Phone, p.Email
		}
		rs.Post("/api/v1/commercial/package-bookings", body)
	}
}

func trialPromotionsDay(_ context.Context, t *Trial, day time.Time) error {
	if day.Weekday() == time.Wednesday {
		t.At(day, "14:00")
		trialFamilyBookings(t, day)
	}
	if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		// the families arrive: pool entry at 10:00, lunch at 12:30
		rs := t.As(trialReservation)
		outlet := t.ids("outlet", "/api/v1/commercial/outlets?limit=100")("RESTO")
		ds := day.Format(time.DateOnly)
		for _, at := range []string{"10:00", "12:30"} {
			t.At(day, at)
			for _, b := range rs.Items("/api/v1/commercial/package-bookings?limit=200&filter[status]=confirmed&date=" + ds) {
				if b.S("startDate") != ds {
					continue
				}
				bd := rs.Get("/api/v1/commercial/package-bookings/" + b.S("id"))
				for _, c := range bd.A("components") {
					if (at == "10:00") != (c.S("componentType") == "service") || c.S("status") != "unused" {
						continue
					}
					body := J{"bookingComponentId": c.S("id"), "reference": "Family Day " + ds}
					if c.S("componentType") == "fnb" {
						body["outletId"] = outlet
					}
					rs.Post("/api/v1/commercial/package-bookings/"+b.S("id")+"/consumption", body)
				}
			}
		}
	}
	return nil
}

// trialPromotionsFinal books the Family Days of the next weekends.
func trialPromotionsFinal(_ context.Context, t *Trial) error {
	for d := t.Today; d.Before(t.Today.AddDate(0, 0, min(t.Ahead, 21))); d = d.AddDate(0, 0, 7) {
		trialFamilyBookings(t, d)
	}
	return nil
}
