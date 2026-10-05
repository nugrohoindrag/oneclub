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

var trialAllDay = J{"weekday": []string{"06:00", "21:00"}, "weekend": []string{"06:00", "21:00"}, "holiday": []string{"06:00", "21:00"}}

func trialLeisureSetup(ctx context.Context, t *Trial) error {
	admin, sm := t.Admin(), t.As(trialSportManager)
	eff := t.Start.AddDate(0, 0, -30).Format(time.DateOnly)
	set := admin.Post("/api/v1/commercial/day-type-sets", J{"code": "SPORT", "name": "Sport Club days"}).S("id")
	wd := admin.Post("/api/v1/commercial/line-day-types", J{"code": "SC-WEEKDAY", "name": "Weekday", "dayTypeSetId": set, "weekdays": "1,2,3,4,5"}).S("id")
	we := admin.Post("/api/v1/commercial/line-day-types", J{"code": "SC-WEEKEND", "name": "Weekend / Public Holiday", "dayTypeSetId": set, "weekdays": "6,7",
		"includesHolidays": true}).S("id")
	rule := func(b J) {
		b["effectiveFrom"], b["taxCodes"], b["pricingMode"] = eff, []string{"PPN"}, "nett"
		admin.Post("/api/v1/commercial/pricing-rules", b)
	}
	for _, e := range []struct{ item, seg, wd, we string }{
		{"POOL", "walk_in", "85000", "120000"}, {"POOL", "child", "50000", "70000"}, {"POOL", "family", "250000", "350000"},
		{"GYM", "walk_in", "100000", "100000"}, {"POOL", "staying_guest", "0", "0"}} {
		rule(J{"code": e.item + "-" + e.seg + "-WD", "name": e.item + " " + e.seg + " weekday", "serviceType": "facility_entry", "itemRef": e.item,
			"segment": e.seg, "lineDayTypeId": wd, "unit": "entry", "price": e.wd, "revenueComponent": "sport_entry"})
		rule(J{"code": e.item + "-" + e.seg + "-WE", "name": e.item + " " + e.seg + " weekend", "serviceType": "facility_entry", "itemRef": e.item,
			"segment": e.seg, "lineDayTypeId": we, "unit": "entry", "price": e.we, "revenueComponent": "sport_entry"})
	}
	rule(J{"code": "TENNIS-HOUR", "name": "Tennis court per hour", "serviceType": "sport_court", "itemRef": "TENNIS", "unit": "slot", "unitMinutes": 60,
		"price": "150000", "revenueComponent": "court"})
	rule(J{"code": "SWIM-REG", "name": "Swimming class registration", "serviceType": "class_registration", "itemRef": "SWIM-KIDS", "unit": "registration",
		"price": "150000", "revenueComponent": "registration_fee"})
	rule(J{"code": "TENNIS-REG", "name": "Tennis clinic registration", "serviceType": "class_registration", "itemRef": "TENNIS-CLINIC",
		"unit": "registration", "price": "200000", "revenueComponent": "registration_fee"})
	for _, f := range []J{
		{"code": "POOL", "name": "Olympic Pool", "facilityType": "swimming_pool", "usageMode": "entry", "capacity": 150, "priceItem": "POOL"},
		{"code": "GYM", "name": "Fitness Center", "facilityType": "gym", "usageMode": "entry", "capacity": 40, "priceItem": "GYM"},
		{"code": "TENNIS", "name": "Tennis Courts", "facilityType": "tennis", "usageMode": "slot_booking", "priceItem": "TENNIS"},
		{"code": "POOL-TRAIN", "name": "Training Pool", "facilityType": "swimming_pool", "usageMode": "class"},
	} {
		f["openingHours"] = trialAllDay
		id := sm.Post("/api/v1/sportclub/facilities", f).S("id")
		t.SetRef("facility:"+f.S("code"), id)
	}
	for _, c := range []string{"T1", "T2", "T3"} {
		sm.Post("/api/v1/sportclub/courts", J{"code": "TENNIS-" + c, "name": "Tennis Court " + c, "facilityId": t.Ref("facility:TENNIS")})
	}
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
	swim := sm.Post("/api/v1/sportclub/class-programs", J{"code": "SWIM-KIDS", "name": "Swimming Kids", "discipline": "swimming", "capacity": 10,
		"durationMinutes": 60, "facilityId": t.Ref("facility:POOL-TRAIN"), "registrationValidMonths": 12}).S("id")
	tennis := sm.Post("/api/v1/sportclub/class-programs", J{"code": "TENNIS-CLINIC", "name": "Tennis Clinic", "discipline": "tennis", "capacity": 8,
		"durationMinutes": 90, "registrationValidMonths": 12}).S("id")
	until := t.Today.AddDate(0, 0, t.Ahead).Format(time.DateOnly)
	from := t.Start.Format(time.DateOnly)
	sm.Post("/api/v1/sportclub/class-schedules", J{"programId": swim, "instructorId": swimCoach, "facilityId": t.Ref("facility:POOL-TRAIN"),
		"weekdays": []int{2, 4, 6}, "startTime": "16:00", "startDate": from, "endDate": until})
	sm.Post("/api/v1/sportclub/class-schedules", J{"programId": tennis, "instructorId": tennisCoach, "weekdays": []int{3, 6}, "startTime": "08:00",
		"startDate": from, "endDate": until})
	for _, v := range []J{
		{"code": "POOL5", "name": "Voucher Kolam 5x", "kind": "quota", "category": "sport_entry", "unit": "entry", "faceValue": "5", "price": "375000",
			"applicableServices": []string{"facility_entry"}, "validityMonths": 3},
		{"code": "SWIM-4X", "name": "Swimming Kids 4x", "kind": "quota", "category": "class_package", "unit": "session", "faceValue": "4", "price": "450000",
			"applicableItems": []string{"SWIM-KIDS"}, "revenueComponent": "class", "validityMonths": 2},
		{"code": "TENNIS-4X", "name": "Tennis Clinic 4x", "kind": "quota", "category": "class_package", "unit": "session", "faceValue": "4", "price": "600000",
			"applicableItems": []string{"TENNIS-CLINIC"}, "revenueComponent": "class", "validityMonths": 2},
	} {
		t.SetRef("vouchertype:"+v.S("code"), admin.Post("/api/v1/commercial/voucher-types", v).S("id"))
	}
	// students of the classes (guests of the club)
	guests := t.Guests()
	rc := t.As(trialReception)
	for i := range 14 {
		g := guests[(i*7+3)%len(guests)]
		prog, pkg := swim, "SWIM-4X"
		if i%3 == 2 {
			prog, pkg = tennis, "TENNIS-4X"
		}
		rc.Post("/api/v1/sportclub/enrollments", J{"programId": prog, "customerId": g.ID, "payment": J{"methodType": "bank_transfer", "reference": "REG-" + fmt.Sprint(i)}})
		rc.Post("/api/v1/commercial/vouchers:sell", J{"voucherTypeId": t.Ref("vouchertype:" + pkg), "customerId": g.ID, "count": 2,
			"payment": J{"methodType": "bank_transfer", "reference": "PKG-" + fmt.Sprint(i)}})
	}

	// bungalows
	rm := t.As(trialResort)
	ro := admin.Post("/api/v1/commercial/rate-plans", J{"code": "STAY_RO", "name": "Room Only", "serviceType": "bungalow", "minNights": 1,
		"facilityAccess": []string{"swimming_pool", "gym"}}).S("id")
	for _, b := range []struct {
		code, name, price string
		adults            int
		units             []string
	}{{"EAGLE", "Eagle Bungalow", "1450000", 2, []string{"E-01", "E-02", "E-03"}}, {"ALBATROSS", "Albatross Family Bungalow", "2350000", 4, []string{"A-01", "A-02"}}} {
		rule(J{"code": b.code + "-RO", "name": b.name + " Room Only", "serviceType": "bungalow", "itemRef": b.code, "ratePlanId": ro, "unit": "night",
			"price": b.price, "revenueComponent": "bungalow"})
		tid := rm.Post("/api/v1/stay/bungalow-types", J{"code": b.code, "name": b.name, "maxAdults": b.adults, "bedrooms": b.adults / 2}).S("id")
		t.SetRef("bungalowtype:"+b.code, tid)
		for i, u := range b.units {
			rm.Post("/api/v1/stay/bungalows", J{"code": u, "name": b.name + " " + u[2:], "typeId": tid, "view": []string{"golf", "lake"}[i%2]})
		}
	}
	return nil
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
		rc.Post("/api/v1/commercial/vouchers:sell", J{"voucherTypeId": voucherType("POOL5"), "customerId": g.ID, "payment": J{"methodType": "qris"}})
	}
	// tennis court bookings
	courts := t.ids("court", "/api/v1/sportclub/courts?limit=20", trialSportManager)
	t.At(day, "10:00")
	tennis := []string{"TENNIS-T1", "TENNIS-T2", "TENNIS-T3"}
	for i := range 1 + r.IntN(len(tennis)) {
		g := guests[r.IntN(len(guests))]
		start := t.Clock(day, fmt.Sprintf("%02d:00", 15+i))
		rc.Post("/api/v1/sportclub/bookings", J{"courtId": courts(tennis[i%len(tennis)]), "start": start.Format(time.RFC3339),
			"end": start.Add(time.Hour).Format(time.RFC3339), "customerId": g.ID, "payment": J{"methodType": "card", "reference": "EDC-TNS"}})
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
					code := "SWIM-4X"
					if program[s.S("programId")] == "TENNIS-CLINIC" {
						code = "TENNIS-4X"
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
		if st, b, _ := rm.Call("GET", "/api/v1/stay/bungalows/"+s.S("unitId"), nil); st == 200 && b.S("readiness") != "ready" {
			rm.Post("/api/v1/stay/bungalows/"+s.S("unitId")+":readiness", J{"readiness": "ready"})
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
		st, res, _ := rm.Call("POST", "/api/v1/stay/stays", J{"kind": "bungalow", "bungalowTypeId": bungalowType(typ), "arrivalDate": ds,
			"departureDate": day.AddDate(0, 0, nights).Format(time.DateOnly), "ratePlan": "STAY_RO", "customerId": g.ID, "adults": 2,
			"payment": J{"methodType": "bank_transfer", "reference": "DP-" + ds}})
		if st != 201 {
			continue // sold out
		}
		sid := res.M("stay").S("id")
		rm.Post("/api/v1/stay/stays/"+sid+":check-in", J{"idType": "ktp", "idNumber": fmt.Sprintf("3671%012d", r.IntN(1000000000000))})
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
		rm.Call("POST", "/api/v1/stay/stays", J{"kind": "bungalow", "bungalowTypeId": bungalowType("EAGLE"), "arrivalDate": day.Format(time.DateOnly),
			"departureDate": day.AddDate(0, 0, 2).Format(time.DateOnly), "ratePlan": "STAY_RO", "customerId": g.ID, "adults": 2,
			"payment": J{"methodType": "bank_transfer", "reference": "DP-" + day.Format("0102")}})
	}
	return nil
}
