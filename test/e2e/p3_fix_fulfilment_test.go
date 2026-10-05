package e2e

// P3 fulfilment gap fixes (P3 traceability audit, package A): packages
// fulfilled by golf and Stay & Venue, banquet and CRM following package and
// event status, golf course blocks of events and the automatic night
// audit postings.

import (
	"encoding/json"
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
	_ = json.Marshal
}
