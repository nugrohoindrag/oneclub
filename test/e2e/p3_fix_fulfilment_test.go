package e2e

// P3 fulfilment gap fixes (P3 traceability audit, package A): packages
// fulfilled by golf and Stay & Venue, banquet and CRM following package and
// event status, golf course blocks of events and the automatic night
// audit postings.

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// pfOutbox replays the latest outbox event of a type whose payload has
// key = value through a handler (idempotency checks).
func pfOutbox(t *testing.T, eventType, key, value string, h func(tx pgx.Tx, payload []byte) error) {
	t.Helper()
	var payload []byte
	sysQueryRow(t, inst, `SELECT payload FROM platform.outbox WHERE event_type = $1 AND payload->>$2 = $3 ORDER BY occurred_at DESC LIMIT 1`,
		[]any{eventType, key, value}, &payload)
	if err := inst.DB.WithTx(dbtx.System(t.Context()), func(tx pgx.Tx) error { return h(tx, payload) }); err != nil {
		t.Fatal(err)
	}
}

// PRD P3 FR-PKG-04/06/08, §9.2: the tee time component of a package becomes
// a golf booking with its players and the bungalow component a stay on the
// Stay Front Desk (once, however often the event is delivered); golf-side
// and stay-side cancellation follow the package; each golfer checked in
// uses one place of the component and the stay check-in consumes the
// bungalow; cancelling the package cancels the golf booking and the stay.
func TestP3FixFulfilmentPackages(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	rs := roleUser(t, inst, "reservation_staff")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	days := pcWeekdays(40, 2)

	sa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": "PF" + sfx, "name": "PPN nett pf " + sfx, "kind": "tax",
		"ratePercent": "11", "basis": "net_amount", "pricingMode": "nett", "effectiveFrom": past()})
	bt := idOf(sa.Must(201, "POST", "/api/v1/stay/bungalow-types", map[string]any{"code": "PF" + sfx, "name": "Package Villa " + sfx, "maxAdults": 2}))
	bg := sa.Must(201, "POST", "/api/v1/stay/bungalows", map[string]any{"code": "PF-" + sfx, "name": "Villa " + sfx, "typeId": bt}).JSON()
	gc := setupGolfCourse(t, sa, "PF"+sfx[:4])
	var courseCode string
	sysQueryRow(t, inst, `SELECT code FROM golf.courses WHERE id = $1`, []any{mustUUID(gc.Course)}, &courseCode)
	for _, d := range days {
		teeTimes(t, sa, gc.Course, d)
	}
	cust := customer(t, sa, "PFC"+sfx, "Corporate Nominee "+sfx, map[string]any{"email": "pf" + sfx + "@pkg.test", "phone": "+62815" + sfx})
	pid := idOf(sa.Must(201, "POST", "/api/v1/commercial/packages", map[string]any{"code": "PF" + sfx, "name": "Stay & Golf " + sfx,
		"packageType": "stay_golf", "pricingMode": "fixed", "price": "3000000", "minPax": 2, "maxPax": 2, "nights": 1, "startTime": "07:00",
		"taxMode": "nett", "taxCodes": []string{"PF" + sfx}, "cancellationHours": 0}))
	sa.Must(201, "POST", "/api/v1/commercial/package-components", map[string]any{"packageId": pid, "seq": 1, "componentType": "reservation",
		"name": "Villa", "resourceTypeCode": "bungalow", "resourceId": bg["resourceId"], "standalonePrice": "1500000", "revenueComponent": "bungalow",
		"businessLine": "stay"})
	sa.Must(201, "POST", "/api/v1/commercial/package-components", map[string]any{"packageId": pid, "seq": 2, "componentType": "tee_time",
		"name": "Green Fee", "refCode": courseCode, "perPax": true, "durationMinutes": 30, "standalonePrice": "1500000", "revenueComponent": "green_fee",
		"businessLine": "golf"})
	sa.Must(200, "POST", "/api/v1/commercial/packages/"+pid+":publish", nil)

	book := func(day string) (string, map[string]map[string]any) {
		b := rs.Must(201, "POST", "/api/v1/commercial/package-bookings", map[string]any{"packageId": pid, "startDate": day, "pax": 2, "customerId": cust},
			"Idempotency-Key", newKey()).JSON()
		if b["status"] != "confirmed" {
			t.Fatalf("package booking: %v", b)
		}
		bid := str(b["id"])
		pcDispatch(t, "golf booking and stay of the package", func() bool {
			var golfN, stays int
			sysQueryRow(t, inst, `SELECT (SELECT count(*) FROM golf.bookings WHERE package_booking_id = $1), (SELECT count(*) FROM stay.stays
				WHERE package_booking_id = $1)`, []any{mustUUID(bid)}, &golfN, &stays)
			return golfN == 1 && stays == 1
		})
		return bid, pkgComponents(b)
	}
	ids := func(bid string) (string, string) {
		var gid, sid uuid.UUID
		sysQueryRow(t, inst, `SELECT (SELECT id FROM golf.bookings WHERE package_booking_id = $1), (SELECT id FROM stay.stays WHERE package_booking_id = $1)`,
			[]any{mustUUID(bid)}, &gid, &sid)
		return gid.String(), sid.String()
	}

	// ── the package is fulfilled in golf and Stay & Venue ────────────────
	bid, cs := book(days[0])
	gid, sid := ids(bid)
	g := sa.Must(200, "GET", "/api/v1/golf/bookings/"+gid, nil).JSON()
	players := asMaps(g["players"])
	if g["status"] != "confirmed" || g["packageBookingId"] != bid || len(players) != 2 || g["folioId"] != nil || players[0]["customerId"] != cust ||
		players[0]["tba"] != false || players[1]["tba"] != true || g["packageComponentId"] != cs["Green Fee"]["id"] {
		t.Fatalf("golf booking of the package: %v", g)
	}
	st := sa.Must(200, "GET", "/api/v1/stay/stays/"+sid, nil).JSON()["stay"].(map[string]any)
	if st["status"] != "reserved" || st["unitId"] != bg["id"] || st["packageBookingId"] != bid || st["roomPosting"] != "package" || st["folioId"] == nil {
		t.Fatalf("stay of the package: %v", st)
	}
	if l := sa.Must(200, "GET", "/api/v1/stay/stays?filter[kind]=bungalow&limit=200", nil).Items(); !containsID(l, sid) {
		t.Fatal("the package stay is on the Stay Front Desk")
	}
	// delivered again: nothing new
	pfOutbox(t, "commercial.package_booked", "bookingId", bid, func(tx pgx.Tx, payload []byte) error {
		ev := outboxEvent("commercial.package_booked", payload, inst.Main)
		if err := inst.App.Golf.OnPackageBooked(dbtx.System(t.Context()), tx, ev); err != nil {
			return err
		}
		return inst.App.Stay.OnPackageBooked(dbtx.System(t.Context()), tx, ev)
	})
	if a, b := ids(bid); a != gid || b != sid {
		t.Fatal("package fulfilment is not idempotent")
	}
	var golfN, stayN int
	sysQueryRow(t, inst, `SELECT (SELECT count(*) FROM golf.bookings WHERE package_booking_id = $1), (SELECT count(*) FROM stay.stays WHERE package_booking_id = $1)`,
		[]any{mustUUID(bid)}, &golfN, &stayN)
	if golfN != 1 || stayN != 1 {
		t.Fatalf("delivered twice: %d golf bookings, %d stays", golfN, stayN)
	}
	// changes follow the package (FR-PKG-08)
	sa.Must(409, "POST", "/api/v1/golf/bookings/"+gid+":cancel", map[string]any{"reason": "change of plan"})
	sa.Must(409, "POST", "/api/v1/stay/stays/"+sid+":cancel", map[string]any{"reason": "change of plan"})

	// ── nominee check-in consumes the component per golfer (FR-PKG-06) ──
	p1, p2 := str(players[0]["id"]), str(players[1]["id"])
	ci := sa.Must(200, "POST", "/api/v1/golf/check-ins", map[string]any{"bookingId": gid, "playerIds": []string{p1}}).JSON()
	if len(ci["checkedIn"].([]any)) != 1 {
		t.Fatalf("check-in of the package golfer: %v", ci)
	}
	pcDispatch(t, "one place of the tee time used", func() bool {
		c := pkgComponents(rs.Must(200, "GET", "/api/v1/commercial/package-bookings/"+bid, nil).JSON())["Green Fee"]
		return dec(c["consumedQuantity"]).Equal(decimal.NewFromInt(1)) && c["status"] == "unused"
	})
	sa.Must(409, "POST", "/api/v1/golf/check-ins", map[string]any{"bookingId": gid, "playerIds": []string{p2}}) // TBA nominee
	sa.Must(200, "PATCH", "/api/v1/golf/bookings/"+gid+"/players/"+p2, map[string]any{"name": "Nominee Two " + sfx})
	sa.Must(200, "POST", "/api/v1/golf/check-ins", map[string]any{"bookingId": gid, "playerIds": []string{p2}})
	pcDispatch(t, "tee time component consumed", func() bool {
		return pkgComponents(rs.Must(200, "GET", "/api/v1/commercial/package-bookings/"+bid, nil).JSON())["Green Fee"]["status"] == "consumed"
	})
	var consumptions int
	sysQueryRow(t, inst, `SELECT count(*) FROM commercial.package_consumptions WHERE booking_component_id = $1`, []any{mustUUID(str(cs["Green Fee"]["id"]))},
		&consumptions)
	if consumptions != 2 {
		t.Fatalf("one consumption per golfer: %d", consumptions)
	}
	// the stay check-in consumes the bungalow (reservation.checked_in)
	sci := sa.Must(200, "POST", "/api/v1/stay/stays/"+sid+":check-in", map[string]any{"idType": "ktp", "idNumber": "3171010101010001"}).JSON()
	if sci["stay"].(map[string]any)["status"] != "checked_in" {
		t.Fatalf("package stay check-in: %v", sci)
	}
	pcDispatch(t, "bungalow component consumed", func() bool {
		return pkgComponents(rs.Must(200, "GET", "/api/v1/commercial/package-bookings/"+bid, nil).JSON())["Villa"]["status"] == "consumed"
	})

	// ── cancelling the package cancels the golf booking and the stay ────
	bid2, _ := book(days[1])
	gid2, sid2 := ids(bid2)
	rs.Must(200, "POST", "/api/v1/commercial/package-bookings/"+bid2+":cancel", map[string]any{"reason": "event postponed"})
	pcDispatch(t, "golf booking and stay cancelled with the package", func() bool {
		return sa.Must(200, "GET", "/api/v1/golf/bookings/"+gid2, nil).JSON()["status"] == "cancelled" &&
			sa.Must(200, "GET", "/api/v1/stay/stays/"+sid2, nil).JSON()["stay"].(map[string]any)["status"] == "cancelled"
	})
	if pl := asMaps(sa.Must(200, "GET", "/api/v1/golf/bookings/"+gid2, nil).JSON()["players"]); pl[0]["status"] != "cancelled" {
		t.Fatalf("players of the cancelled package: %v", pl)
	}
}

// pfPublish publishes a domain event through the outbox of the instance
// (as the owning module would) so the wired subscribers receive it.
func pfPublish(t *testing.T, eventType, aggregate string, property uuid.UUID, payload map[string]any) {
	t.Helper()
	ctx := dbtx.System(t.Context())
	agg := uuid.New()
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := inst.App.Bus.Publish(ctx, tx, eventType, aggregate, &agg, &property, payload)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// PRD P3 FR-PKG-08: the event of the banquet component of a package follows
// commercial.package_cancelled — cancelled with it, or back to Tentative
// with an option date under Banquet Policies "tentative".
func TestP3FixFulfilmentBanquetPackageCancel(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	cust := customer(t, sa, "PFB"+sfx, "Package Couple "+sfx, map[string]any{"email": "pfb" + sfx + "@pkg.test"})
	day := time.Now().In(clubLoc(inst)).AddDate(0, 0, 75).Format("2006-01-02")
	n := 0
	book := func() (string, string) {
		n++
		bid, comp := uuid.NewString(), uuid.NewString()
		pfPublish(t, "commercial.package_booked", "commercial.package_booking", inst.Main, map[string]any{"bookingId": bid,
			"number": fmt.Sprintf("PKF-%s-%d", sfx, n), "packageCode": "WED-PF", "customerId": cust, "startDate": day, "endDate": day, "pax": 80,
			"components": []map[string]any{{"bookingComponentId": comp, "componentType": "banquet", "serviceDate": day, "quantity": "1"}}})
		var eid uuid.UUID
		pcDispatch(t, "package event", func() bool {
			sysQueryRow(t, inst, `SELECT coalesce((SELECT id FROM banquet.events WHERE package_component_id = $1), '00000000-0000-0000-0000-000000000000')`,
				[]any{mustUUID(comp)}, &eid)
			return eid != uuid.Nil
		})
		if ev := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid.String(), nil).JSON(); ev["status"] != "definite" {
			t.Fatalf("package event: %v", ev["status"])
		}
		return bid, eid.String()
	}
	cancel := func(bid string) {
		pfPublish(t, "commercial.package_cancelled", "commercial.package_booking", inst.Main, map[string]any{"bookingId": bid, "number": "PKF-" + sfx,
			"status": "cancelled", "reason": "couple postponed"})
	}
	bid, eid := book()
	cancel(bid)
	pcDispatch(t, "event cancelled with the package", func() bool {
		return sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON()["status"] == "cancelled"
	})
	if ev := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid, nil).JSON(); !dec(ev["cancellationFee"]).IsZero() {
		t.Fatalf("nothing forfeited on the event (the package policy applies): %v", ev["cancellationFee"])
	}
	pcPolicy(t, sa, "Banquet Policies", "banquet.booking", map[string]any{"packageCancellation": "tentative"})
	t.Cleanup(func() { pcPolicy(t, sa, "Banquet Policies", "banquet.booking", map[string]any{"packageCancellation": "cancel"}) })
	bid2, eid2 := book()
	cancel(bid2)
	pcDispatch(t, "event back to tentative", func() bool {
		return sa.Must(200, "GET", "/api/v1/banquet/events/"+eid2, nil).JSON()["status"] == "tentative"
	})
	if ev := sa.Must(200, "GET", "/api/v1/banquet/events/"+eid2, nil).JSON(); ev["optionDate"] == nil {
		t.Fatalf("tentative event has an option date: %v", ev)
	}
}

// PRD P3 FR-BQT-14: the sales pipeline follows the event converted from an
// accepted quotation — the confirmation is recorded on the opportunity
// once, the cancellation closes the opportunity as Lost (cancelled).
func TestP3FixFulfilmentCRMBanquetSync(t *testing.T) {
	sa := superAdmin(t, inst)
	bs := roleUser(t, inst, "banquet_sales")
	bsID := slsUserID(t, "role.banquet_sales@matrix.test")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "PFS" + sfx, "name": "Sari & Bima " + sfx,
		"email": "sari" + sfx + "@quote.test"}))
	hall := bqVenue(t, sa, map[string]any{"code": "PS" + sfx, "name": "Sync Hall " + sfx, "venueType": "ballroom", "maxCapacity": 300})
	loc := clubLoc(inst)
	today := time.Now().In(loc)
	day := today.AddDate(0, 5, 0)
	opp := idOf(bs.Must(201, "POST", "/api/v1/crm/opportunities", map[string]any{"title": "Wedding Sari & Bima " + sfx, "line": "wedding",
		"customerId": cust, "ownerUserId": bsID}))
	q := bs.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "opportunityId": opp, "title": "Wedding Sari & Bima " + sfx,
		"line": "wedding", "eventType": "wedding", "eventDate": day.Format("2006-01-02"), "pax": 150, "venueResourceId": hall["resourceId"],
		"optionDate": today.AddDate(0, 0, 5).Format("2006-01-02"), "ownerUserId": bsID, "pricingMode": "nett",
		"paymentTerms": []map[string]any{{"label": "DP 30%", "percent": "30", "dueDays": 3}, {"label": "Final Payment", "percent": "70", "dueDays": 30}},
		"lines": []map[string]any{{"itemType": "banquet_package", "description": "Wedding package 150 pax", "quantity": "1", "unitPrice": "45000000"}}},
		"Idempotency-Key", newKey()).JSON()
	bs.Must(200, "POST", "/api/v1/crm/quotations/"+str(q["id"])+":send", map[string]any{})
	bs.Must(200, "POST", "/api/v1/crm/quotations/"+str(q["id"])+":accept", map[string]any{"acceptedByName": "Sari"})
	var eid string
	bqDispatch(t, "event converted from the quotation", func() bool {
		var conv bool
		eid, _ = bqQuotationEvent(t, q["number"])
		if eid == "" {
			return false
		}
		sysQueryRow(t, inst, `SELECT converted_at IS NOT NULL FROM banquet.events WHERE id = $1`, []any{mustUUID(eid)}, &conv)
		return conv
	})
	dr := bs.Must(200, "POST", "/api/v1/banquet/events/"+eid+":make-definite", map[string]any{"override": true, "reason": "Family of a member"}).JSON()
	if dr["approvalStatus"] == "pending" {
		gm := roleUser(t, inst, "general_manager")
		gm.Must(200, "POST", "/api/v1/platform/approvals/"+str(dr["approvalRequestId"])+":approve", map[string]any{"reason": "OK"})
	}
	confirmed := func() int {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM crm.sales_activities WHERE opportunity_id = $1 AND external_ref = $2`,
			[]any{mustUUID(opp), "banquet.event_confirmed:" + eid}, &n)
		return n
	}
	bqDispatch(t, "confirmation recorded on the opportunity", func() bool { return confirmed() == 1 })
	pfOutbox(t, "banquet.event_confirmed", "eventId", eid, func(tx pgx.Tx, payload []byte) error {
		return inst.App.Sales.Module.OnBanquetEvent(dbtx.System(t.Context()), tx, outboxEvent("banquet.event_confirmed", payload, inst.Main))
	})
	if n := confirmed(); n != 1 {
		t.Fatalf("confirmation recorded once: %d", n)
	}
	if o := bs.Must(200, "GET", "/api/v1/crm/opportunities/"+opp, nil).JSON(); o["status"] == "lost" {
		t.Fatalf("opportunity of a confirmed event: %v", o["status"])
	}
	sa.Must(200, "POST", "/api/v1/banquet/events/"+eid+":cancel", map[string]any{"reason": "Wedding called off"})
	bqDispatch(t, "opportunity lost with the event", func() bool {
		o := bs.Must(200, "GET", "/api/v1/crm/opportunities/"+opp, nil).JSON()
		return o["status"] == "lost" && o["lostReason"] == "cancelled"
	})
	var cancelled int
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.sales_activities WHERE opportunity_id = $1 AND external_ref = $2`,
		[]any{mustUUID(opp), "banquet.event_cancelled:" + eid}, &cancelled)
	if cancelled != 1 {
		t.Fatalf("cancellation recorded on the opportunity: %d", cancelled)
	}
}
