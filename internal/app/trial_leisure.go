package app

// Trial dataset: sport club and stay. At go-live the sport club gets its
// facilities (pool, gym, tennis courts, training pool), prices, coaches,
// classes with their weekly schedule and entry vouchers; the bungalows get
// their types and rates. Every simulated day visitors enter the pool and
// the gym (walk-in, child, family package, entry voucher), tennis courts
// are booked, the classes run with bookings and attendance, vouchers are
// sold; guests stay in the bungalows (deposit, check-in with ID, the night
// audit posts the room nights, check-out after settling the folio). Every
// month the coaches' fees are calculated, approved and paid.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "leisure", Order: 40, Setup: trialLeisureSetup, Day: trialLeisureDay, Final: trialLeisureFinal})
}

const (
	trialSportManager = "sport.manager@demo.oneclub.id"
	trialReception    = "sport.reception@demo.oneclub.id"
	trialCoach        = "coach@demo.oneclub.id"
	trialResort       = "resort.manager@demo.oneclub.id"
)

func trialLeisureSetup(ctx context.Context, t *Trial) error {
	admin, sm := t.Admin(), t.As(trialSportManager)
	eff := t.Start.AddDate(0, 0, -30).Format(time.DateOnly)
	rule := func(b J) {
		b["effectiveFrom"], b["taxCodes"], b["pricingMode"] = eff, []string{"PPN"}, "nett"
		admin.Post("/api/v1/commercial/pricing-rules", b)
	}
	// facilities, courts, rates, packages and memberships of the brochure (Rates 2025)
	trialSportClubMGCC(t)
	var coachUser string
	if err := t.App.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM platform.users WHERE email = $1`, trialCoach).Scan(&coachUser)
	}); err != nil {
		return err
	}
	coach := J{"code": "COACH-RIZKY", "name": "Coach Rizky", "disciplines": []string{"swimming"}, "feeScheme": "per_session", "feeRate": "150000",
		"partnership": "partner"}
	if coachUser != "" {
		coach["userId"] = coachUser
	}
	swimCoach := sm.Post("/api/v1/sportclub/instructors", coach).S("id")
	tennisCoach := sm.Post("/api/v1/sportclub/instructors", J{"code": "COACH-ANDRE", "name": "Coach Andre", "disciplines": []string{"tennis"},
		"feeScheme": "per_student", "feeRate": "60000", "partnership": "partner"}).S("id")
	program := t.ids("program", "/api/v1/sportclub/class-programs?limit=50", trialSportManager)
	swim, tennis := program("SWIM-KIDS"), program("TENNIS-CLINIC")
	until := t.Today.AddDate(0, 0, t.Ahead).Format(time.DateOnly)
	from := t.Start.Format(time.DateOnly)
	sm.Post("/api/v1/sportclub/class-schedules", J{"programId": swim, "instructorId": swimCoach, "facilityId": t.Ref("facility:POOL"),
		"weekdays": []int{2, 4, 6}, "startTime": "16:00", "startDate": from, "endDate": until})
	sm.Post("/api/v1/sportclub/class-schedules", J{"programId": tennis, "instructorId": tennisCoach, "weekdays": []int{3, 6}, "startTime": "08:00",
		"startDate": from, "endDate": until})
	// students of the classes (guests of the club: guest registration and packages)
	guests := t.Guests()
	rc := t.As(trialReception)
	for i := range 14 {
		g := guests[(i*7+3)%len(guests)]
		prog, pkg := swim, "SWIM-G-4X"
		if i%3 == 2 {
			prog, pkg = tennis, "TENNIS-G-4X"
		}
		rc.Post("/api/v1/sportclub/enrollments", J{"programId": prog, "customerId": g.ID, "segment": "guest",
			"payment": J{"methodType": "bank_transfer", "reference": "REG-" + fmt.Sprint(i)}})
		rc.Post("/api/v1/commercial/vouchers:sell", J{"voucherTypeId": t.Ref("vouchertype:" + pkg), "customerId": g.ID, "count": 2,
			"payment": J{"methodType": "bank_transfer", "reference": "PKG-" + fmt.Sprint(i)}})
	}

	// bungalows
	rm := t.As(trialResort)
	ro := admin.Post("/api/v1/commercial/rate-plans", J{"code": "STAY_RO", "name": "Room Only", "serviceType": "bungalow", "minNights": 1,
		"facilityAccess": []string{"swimming_pool", "gym"}}).S("id")
	for _, b := range []struct {
		code, name, price, weekend, bed, size string
		adults                                int
		units                                 []string
	}{{"EAGLE", "Eagle Bungalow", "1450000", "1750000", "1 King", "42", 2, []string{"E-01", "E-02", "E-03"}},
		{"ALBATROSS", "Albatross Family Bungalow", "2350000", "2750000", "1 King + 2 Single", "68", 4, []string{"A-01", "A-02"}}} {
		rule(J{"code": b.code + "-RO", "name": b.name + " Room Only", "serviceType": "bungalow", "itemRef": b.code, "ratePlanId": ro, "unit": "night",
			"price": b.price, "revenueComponent": "bungalow"})
		tid := rm.Post("/api/v1/stay/bungalow-types", J{"code": b.code, "name": b.name, "maxAdults": b.adults, "maxChildren": 2, "bedrooms": b.adults / 2,
			"baseRate": b.price, "weekendRate": b.weekend, "bedConfiguration": b.bed, "sizeSqm": b.size, "view": "Golf course & lake",
			"facilities": []string{"wifi", "ac", "minibar", "terrace", "hot_water"}}).S("id")
		t.SetRef("bungalowtype:"+b.code, tid)
		for i, u := range b.units {
			rm.Post("/api/v1/stay/bungalows", J{"code": u, "name": b.name + " " + u[2:], "typeId": tid, "view": []string{"golf", "lake"}[i%2],
				"location": []string{"Lakeside", "Fairway 9"}[i%2]})
		}
	}
	trialAccommodationSetup(t, rm)
	return nil
}

// trialAccommodationSetup configures the accommodation (bungalow management
// requirements): rate plans, seasons, add-ons, a stay package, promotions and
// preventive maintenance.
func trialAccommodationSetup(t *Trial, rm *TrialClient) {
	pool := []string{"swimming_pool", "gym"}
	rm.Post("/api/v1/stay/rate-plans", J{"code": "BAR", "name": "Best Available Rate", "planType": "standard", "derivation": "bar", "isDefault": true,
		"freeCancelHours": 48, "cancelFeePercent": "50", "noShowFeePercent": "100", "facilityAccess": pool, "sortOrder": 1})
	rm.Post("/api/v1/stay/rate-plans", J{"code": "MEMBER", "name": "Member Rate", "planType": "member", "derivation": "percent", "adjustValue": "-15",
		"eligibility": "member", "includesBreakfast": true, "freeCancelHours": 24, "facilityAccess": []string{"*"}, "sortOrder": 2})
	rm.Post("/api/v1/stay/rate-plans", J{"code": "NONREF", "name": "Non-refundable Saver", "planType": "promotional", "derivation": "percent", "adjustValue": "-10",
		"nonRefundable": true, "paymentPolicy": "full_prepayment", "facilityAccess": pool, "sortOrder": 3})
	rm.Post("/api/v1/stay/rate-plans", J{"code": "CORP", "name": "Corporate Rate", "planType": "corporate", "derivation": "percent", "adjustValue": "-12",
		"eligibility": "corporate", "paymentPolicy": "pay_at_hotel", "facilityAccess": pool, "sortOrder": 4})
	y := t.Today.Year()
	hol := rm.Post("/api/v1/stay/seasons", J{"code": "HOLIDAY", "name": "Year-end Holiday", "seasonType": "holiday", "startDate": fmt.Sprintf("%d-12-20", y),
		"endDate": fmt.Sprintf("%d-01-05", y+1), "priority": 10}).S("id")
	rm.Post("/api/v1/stay/season-prices", J{"seasonId": hol, "bungalowTypeId": t.Ref("bungalowtype:EAGLE"), "weekdayPrice": "1950000", "weekendPrice": "2150000"})
	rm.Post("/api/v1/stay/season-prices", J{"seasonId": hol, "bungalowTypeId": t.Ref("bungalowtype:ALBATROSS"), "weekdayPrice": "3100000", "weekendPrice": "3400000"})
	rm.Post("/api/v1/stay/seasons", J{"code": "PEAK", "name": "School Holiday Peak", "seasonType": "peak", "startDate": fmt.Sprintf("%d-06-20", y),
		"endDate": fmt.Sprintf("%d-07-20", y), "priority": 20, "adjustPercent": "15"})
	addon := func(code, name, cat, price, unit, avail string) string {
		return rm.Post("/api/v1/stay/addons", J{"code": code, "name": name, "category": cat, "price": price, "unit": unit, "availability": avail,
			"pricingMode": "nett"}).S("id")
	}
	bf := addon("BREAKFAST", "Breakfast", "breakfast", "125000", "per_person_night", "both")
	t.SetRef("addon:BREAKFAST", bf)
	addon("EXTRA-BED", "Extra Bed", "extra_bed", "350000", "per_night", "both")
	addon("BBQ", "BBQ Dinner Set", "bbq", "450000", "per_item", "both")
	addon("AIRPORT", "Airport Transfer", "transportation", "300000", "per_item", "both")
	addon("LAUNDRY", "Laundry (per bag)", "laundry", "75000", "per_item", "in_stay")
	addon("TOWEL", "Extra Towel", "extra_towel", "25000", "per_item", "in_stay")
	rm.Post("/api/v1/stay/packages", J{"code": "BNB", "name": "Bed & Breakfast", "priceMode": "fixed", "price": "1700000", "includesBreakfast": true,
		"inclusions": []J{{"addonId": bf, "quantity": 1}}, "bungalowTypeIds": []string{t.Ref("bungalowtype:EAGLE")},
		"description": "Eagle Bungalow with breakfast for two"})
	rm.Post("/api/v1/stay/promotions", J{"code": "STAY3PAY2", "name": "Stay 3 Pay 2", "promoType": "stay_pay", "stayNights": 3, "payNights": 2,
		"bungalowTypeIds": []string{t.Ref("bungalowtype:EAGLE")}, "priority": 10})
	rm.Post("/api/v1/stay/promotions", J{"code": "EARLYBIRD", "name": "Early Bird 10%", "promoType": "early_booking", "minDaysBefore": 30,
		"discountPercent": "10", "priority": 20})
	rm.Post("/api/v1/stay/preventive-schedules", J{"code": "AC-CLEAN", "name": "AC cleaning", "category": "ac", "intervalDays": 90,
		"nextDue": t.Today.AddDate(0, 0, 3).Format(time.DateOnly), "assignedTo": "Teknisi Resort"})
	rm.Post("/api/v1/stay/preventive-schedules", J{"code": "WATER-HEATER", "name": "Water heater inspection", "category": "water_heater", "intervalDays": 180,
		"nextDue": t.Today.AddDate(0, 0, 10).Format(time.DateOnly), "assignedTo": "Teknisi Resort"})
}

func trialLeisureDay(ctx context.Context, t *Trial, day time.Time) error {
	r := t.Rand("leisure:" + day.Format(time.DateOnly))
	rc := t.As(trialReception)
	weekend := day.Weekday() == time.Saturday || day.Weekday() == time.Sunday
	guests := t.Guests()
	t.At(day, "09:00")
	facility := t.ids("facility", "/api/v1/sportclub/facilities?limit=50", trialSportManager)
	voucherType := t.ids("vouchertype", "/api/v1/commercial/voucher-types?limit=100")
	// entries (pool and gym) and voucher sales
	var entries []J
	n := 3 + r.IntN(4)
	if weekend {
		n = 7 + r.IntN(5)
	}
	for range n {
		g := guests[r.IntN(len(guests))]
		pay := J{"methodType": []string{"cash", "qris", "card"}[r.IntN(3)]}
		switch x := r.IntN(10); {
		case x < 5:
			entries = append(entries, J{"facilityId": facility("POOL"), "entryType": "walk_in_guest", "customerId": g.ID, "payment": pay})
		case x < 7:
			entries = append(entries, J{"facilityId": facility("GYM"), "entryType": "walk_in_guest", "customerId": g.ID, "payment": pay})
		case x < 8:
			entries = append(entries, J{"facilityId": facility("POOL"), "entryType": "child", "guest": J{"name": "Anak " + g.Name}, "birthDate": "2017-06-01",
				"payment": pay})
		default:
			entries = append(entries, J{"facilityId": facility("POOL"), "entryType": "family_package", "adults": 2, "children": 1 + r.IntN(2),
				"guest": J{"name": "Keluarga " + g.Name}, "payment": pay})
		}
	}
	t.Parallel(len(entries), trialWorkers, func(i int) { rc.Post("/api/v1/sportclub/entries", entries[i]) })
	if r.IntN(3) == 0 {
		g := guests[r.IntN(len(guests))]
		code := "ENTRY5-WD"
		if weekend {
			code = "ENTRY5-WE"
		}
		rc.Post("/api/v1/commercial/vouchers:sell", J{"voucherTypeId": voucherType(code), "customerId": g.ID, "payment": J{"methodType": "qris"}})
	}
	// court bookings: tennis, futsal, basket & volley, basket indoor (brochure courts)
	courts := t.ids("court", "/api/v1/sportclub/courts?limit=50", trialSportManager)
	t.At(day, "10:00")
	for _, c := range []struct {
		courts []string
		from   int
		hours  int
	}{{[]string{"TENNIS-T1", "TENNIS-T2", "TENNIS-T3", "TENNIS-T4"}, 15, 1}, {[]string{"FUTSAL-SG1", "FUTSAL-SG2", "FUTSAL-TF"}, 17, 1},
		{[]string{"BV-1", "BV-2"}, 16, 1}, {[]string{"BI-1", "BI-2"}, 18, 2}} {
		for i := range 1 + r.IntN(len(c.courts)) {
			g := guests[r.IntN(len(guests))]
			start := t.Clock(day, fmt.Sprintf("%02d:00", c.from+i))
			rc.Call("POST", "/api/v1/sportclub/bookings", J{"courtId": courts(c.courts[i%len(c.courts)]), "start": start.Format(time.RFC3339),
				"end": start.Add(time.Duration(c.hours) * time.Hour).Format(time.RFC3339), "customerId": g.ID,
				"payment": J{"methodType": "card", "reference": "EDC-SPT"}})
		}
	}
	// classes: the enrolled students book today's sessions; the coach takes attendance
	t.At(day, "15:30")
	sessions := rc.Items("/api/v1/sportclub/class-sessions?from=" + day.Format(time.DateOnly) + "&days=1&limit=20")
	if len(sessions) > 0 {
		program := map[string]string{}
		for _, p := range rc.Items("/api/v1/sportclub/class-programs?limit=20") {
			program[p.S("id")] = p.S("code")
		}
		enr := rc.Items("/api/v1/sportclub/enrollments?limit=200")
		for _, s := range sessions {
			var bookings []string
			for _, e := range enr {
				if e.S("programId") != s.S("programId") || e.S("status") != "active" || r.IntN(3) == 0 {
					continue
				}
				st, b, _ := rc.Call("POST", "/api/v1/sportclub/session-bookings", J{"sessionId": s.S("id"), "customerId": e.S("customerId")})
				switch st {
				case 201:
					bookings = append(bookings, b.S("id"))
				case 409: // package used up: buy the next one
					code := "SWIM-G-4X"
					if program[s.S("programId")] == "TENNIS-CLINIC" {
						code = "TENNIS-G-4X"
					}
					rc.Post("/api/v1/commercial/vouchers:sell", J{"voucherTypeId": voucherType(code), "customerId": e.S("customerId"),
						"payment": J{"methodType": "qris"}})
					if st, b, _ := rc.Call("POST", "/api/v1/sportclub/session-bookings", J{"sessionId": s.S("id"), "customerId": e.S("customerId")}); st == 201 {
						bookings = append(bookings, b.S("id"))
					}
				}
			}
			coach := t.As(trialSportManager)
			for i, b := range bookings {
				status := "present"
				if i%7 == 6 {
					status = "absent"
				}
				coach.Post("/api/v1/sportclub/attendance", J{"sessionBookingId": b, "status": status})
			}
		}
	}
	// coaches' fees of the previous month
	if day.Day() == 1 {
		trialInstructorFees(t, day)
	}
	trialStayDay(t, day)
	return nil
}

// trialInstructorFees calculates and pays the coaches' fees of the month
// before day.
func trialInstructorFees(t *Trial, day time.Time) {
	sm := t.As(trialSportManager)
	from := day.AddDate(0, -1, 0)
	if from.Before(t.Start) {
		from = t.Start
	}
	for _, in := range sm.Items("/api/v1/sportclub/instructors?limit=20") {
		st, fee, _ := sm.Call("POST", "/api/v1/sportclub/instructor-fees:calculate", J{"instructorId": in.S("id"), "periodStart": from.Format(time.DateOnly),
			"periodEnd": day.AddDate(0, 0, -1).Format(time.DateOnly)})
		if st != 201 {
			continue // no sessions held
		}
		if fee.S("status") != "approved" {
			t.ApprovePending(context.Background())
			fee = sm.Get("/api/v1/sportclub/instructor-fees/" + fee.S("id"))
		}
		if fee.S("status") == "approved" {
			t.As(trialAccountant).Post("/api/v1/sportclub/instructor-fees/"+fee.S("id")+":pay", J{"methodType": "bank_transfer", "reference": "HON-" + day.Format("200601")})
		}
	}
}

// trialStayDay: arrivals (booked with a deposit, checked in with an ID)
// and departures (folio settled, checked out).
func trialStayDay(t *Trial, day time.Time) {
	r := t.Rand("stay:" + day.Format(time.DateOnly))
	rm, cashier := t.As(trialResort), t.As(trialCashier)
	ds := day.Format(time.DateOnly)
	bungalowType := t.ids("bungalowtype", "/api/v1/stay/bungalow-types?limit=20", trialResort)
	// departures of the day
	t.At(day, "11:00")
	for _, s := range rm.Items("/api/v1/stay/stays?filter[status]=checked_in&limit=100") {
		end, err := time.Parse(time.RFC3339, s.S("end"))
		if err != nil || end.In(t.Loc).Format(time.DateOnly) > ds {
			continue
		}
		trialPayFolio(t, cashier, s.S("folioId"), []string{"card", "bank_transfer"}[r.IntN(2)])
		rm.Post("/api/v1/stay/stays/"+s.S("id")+":check-out", J{"at": t.Clock(day, "11:30").Format(time.RFC3339)})
	}
	// housekeeping cleans and inspects the check-out bungalows before the arrivals
	t.At(day, "12:30")
	for _, task := range rm.Get("/api/v1/stay/housekeeping?date=" + ds).A("tasks") {
		if task.S("taskType") != "checkout_cleaning" || task.S("status") == "inspected" || task.S("status") == "cancelled" {
			continue
		}
		path := "/api/v1/stay/housekeeping-tasks/" + task.S("id")
		if task.S("status") == "open" {
			rm.Post(path+":assign", J{"assignedTo": []string{"Sari", "Wati", "Dewi"}[r.IntN(3)]})
			rm.Post(path+":start", nil)
		}
		if task.S("status") != "completed" {
			rm.Post(path+":complete", J{})
		}
		rm.Post(path+":inspect", J{"result": "passed"})
	}
	for _, b := range rm.Items("/api/v1/stay/room-status?date=" + ds) {
		if b.S("hkStatus") != "ready" && b.S("blockKind") == "" && b.S("currentStayId") == "" {
			rm.Post("/api/v1/stay/bungalows/"+b.S("id")+":readiness", J{"readiness": "ready"})
		}
	}
	// arrivals: booked today, checked in in the afternoon
	t.At(day, "14:00")
	weekend := day.Weekday() == time.Friday || day.Weekday() == time.Saturday
	n := r.IntN(2)
	if weekend {
		n = 1 + r.IntN(2)
	}
	guests := t.Guests()
	for range n {
		g := guests[r.IntN(len(guests))]
		typ := []string{"EAGLE", "EAGLE", "ALBATROSS"}[r.IntN(3)]
		nights := 1 + r.IntN(3)
		// half the guests on the accommodation rates (BAR, some with breakfast), half on the P2 Room Only rate
		body := J{"kind": "bungalow", "bungalowTypeId": bungalowType(typ), "arrivalDate": ds, "departureDate": day.AddDate(0, 0, nights).Format(time.DateOnly),
			"ratePlan": "STAY_RO", "customerId": g.ID, "adults": 2, "bookingSource": []string{"front_desk", "phone", "website", "walk_in"}[r.IntN(4)],
			"payment": J{"methodType": "bank_transfer", "reference": "DP-" + ds}}
		accommodation, breakfast := r.IntN(2) == 0, r.IntN(2) == 0
		if accommodation {
			delete(body, "ratePlan")
			if breakfast {
				body["addons"] = []J{{"addonId": t.Ref("addon:BREAKFAST"), "quantity": 1}}
			}
		}
		request := r.IntN(3)
		st, res, _ := rm.Call("POST", "/api/v1/stay/stays", body)
		if st != 201 {
			continue // sold out
		}
		sid := res.M("stay").S("id")
		rm.Post("/api/v1/stay/stays/"+sid+":check-in", J{"idType": "ktp", "idNumber": fmt.Sprintf("3671%012d", r.IntN(1000000000000))})
		if request == 0 {
			rq := rm.Post("/api/v1/stay/guest-requests", J{"stayId": sid, "requestType": []string{"extra_towel", "room_cleaning", "laundry"}[r.IntN(3)],
				"source": "phone"})
			if rq.S("status") != "completed" {
				rm.Post("/api/v1/stay/guest-requests/"+rq.S("id")+":complete", J{})
			}
		}
	}
}

// trialLeisureFinal books upcoming stays.
func trialLeisureFinal(_ context.Context, t *Trial) error {
	rm := t.As(trialResort)
	guests := t.Guests()
	bungalowType := t.ids("bungalowtype", "/api/v1/stay/bungalow-types?limit=20", trialResort)
	for d := 2; d < t.Ahead; d += 3 {
		day := t.Today.AddDate(0, 0, d)
		r := t.Rand("stay-ahead:" + day.Format(time.DateOnly))
		g := guests[r.IntN(len(guests))]
		body := J{"kind": "bungalow", "bungalowTypeId": bungalowType("EAGLE"), "arrivalDate": day.Format(time.DateOnly),
			"departureDate": day.AddDate(0, 0, 2).Format(time.DateOnly), "customerId": g.ID, "adults": 2, "bookingSource": "website",
			"payment": J{"methodType": "bank_transfer", "reference": "DP-" + day.Format("0102")}}
		if d%2 == 0 {
			body["stayPackage"] = "BNB"
		}
		rm.Call("POST", "/api/v1/stay/stays", body)
	}
	return nil
}
