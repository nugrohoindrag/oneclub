package app

// Sport Club of Modern Golf as on the brochure "Sport Club Rates 2025"
// (sport-club-rates.jpg, sportclub-rates-2.jpg; demo feedback 10 Oct 2026
// #38, docs/requirement-booking-sportclub-mgcc.md Lampiran A and §12):
//
//   - courts: Tennis Arena (4 indoor courts under the dome), Futsal Taraflex
//     (1) and Synthetic Grass (2), Basket & Volley Arena (2), Basket Indoor
//     (2); no badminton, squash, table tennis, padel nor ballboy;
//   - court rates per day (Mon–Thu, Fri, Sat, Sun & public holiday) and time
//     (07–16 / 16–21(22), 06–17 / 17–21), Basket Indoor 1 h and 2 h, the
//     4x/8x court packages, all before tax (the tax is added on top);
//   - the Sport Club entry (pool, gym, aerobic studio: one ticket) for walk-in
//     guests, guests with a member, children, families, the 5-time voucher;
//   - the training classes Swimming, Tennis Lapangan and Aikido with their
//     member / guest registration and 4x/8x packages;
//   - the Sport Club membership types;
//   - the demo runs every court 24 hours for two weeks, then the normal
//     hours come back by themselves (like the golf 24-hour demo).
//
// It runs in the leisure setup of a new dataset and once (final step) on a
// dataset seeded before, where it renames and completes the earlier data.

import (
	"context"
	"fmt"
	"time"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "sportclub-mgcc", Order: 41, Final: func(_ context.Context, t *Trial) error { trialSportClubMGCC(t); return nil }})
}

// trialSportEntryItem is the pricing item of the one Sport Club entry ticket.
const trialSportEntryItem = "SPORTCLUB"

func trialSportClubMGCC(t *Trial) {
	admin, sm := t.Admin(), t.As(trialSportManager)
	eff := t.Start.AddDate(0, 0, -30).Format(time.DateOnly)
	byCode := func(c *TrialClient, path string) map[string]J {
		out := map[string]J{}
		for _, x := range c.Items(path) {
			out[x.S("code")] = x
		}
		return out
	}
	ensure := func(c *TrialClient, path string, have map[string]J, body J) string {
		if x, ok := have[body.S("code")]; ok {
			patch := J{}
			for k, v := range body {
				if k != "code" && k != "dayTypeSetId" && k != "kind" && k != "programId" && k != "typeId" {
					patch[k] = v
				}
			}
			c.Try("PATCH", path+"/"+x.S("id"), patch)
			return x.S("id")
		}
		id := c.Post(path, body).S("id")
		have[body.S("code")] = J{"id": id, "code": body.S("code")}
		return id
	}

	// ── days and hours of the rate card ──
	sets := byCode(admin, "/api/v1/commercial/day-type-sets?limit=100")
	entrySet := ensure(admin, "/api/v1/commercial/day-type-sets", sets, J{"code": "SPORT", "name": "Sport Club entry days"})
	courtSet := ensure(admin, "/api/v1/commercial/day-type-sets", sets, J{"code": "SPORT-COURT", "name": "Sport Club court days (Rates 2025)"})
	ldt := byCode(admin, "/api/v1/commercial/line-day-types?limit=200")
	day := map[string]string{}
	for _, d := range []J{
		{"code": "SC-WEEKDAY", "name": "Weekday", "dayTypeSetId": entrySet, "weekdays": "1,2,3,4,5"},
		{"code": "SC-WEEKEND", "name": "Weekend / Public Holiday", "dayTypeSetId": entrySet, "weekdays": "6,7", "includesHolidays": true},
		{"code": "SC-MONTHU", "name": "Monday – Thursday", "dayTypeSetId": courtSet, "weekdays": "1,2,3,4"},
		{"code": "SC-FRI", "name": "Friday", "dayTypeSetId": courtSet, "weekdays": "5"},
		{"code": "SC-SAT", "name": "Saturday", "dayTypeSetId": courtSet, "weekdays": "6"},
		{"code": "SC-SUN", "name": "Sunday / Public Holiday", "dayTypeSetId": courtSet, "weekdays": "7", "includesHolidays": true},
	} {
		day[d.S("code")] = ensure(admin, "/api/v1/commercial/line-day-types", ldt, d)
	}
	// the evening band starts at 16:00 (futsal) or 17:00 (tennis, basket &
	// volley); the day band starts at midnight so the 24-hour demo is priced
	bands := byCode(admin, "/api/v1/commercial/time-bands?limit=100")
	band := map[string]string{}
	for _, b := range []J{
		{"code": "SC-DAY16", "name": "Day 07.00–16.00", "session": "other", "startTime": "00:00", "endTime": "16:00"},
		{"code": "SC-EVE16", "name": "Evening 16.00–22.00", "session": "other", "startTime": "16:00", "endTime": "23:59"},
		{"code": "SC-DAY17", "name": "Day 06.00–17.00", "session": "other", "startTime": "00:00", "endTime": "17:00"},
		{"code": "SC-EVE17", "name": "Evening 17.00–21.00", "session": "other", "startTime": "17:00", "endTime": "23:59"},
	} {
		band[b.S("code")] = ensure(admin, "/api/v1/commercial/time-bands", bands, b)
	}

	// ── rates (before tax: PPN on top) ──
	rules := byCode(admin, "/api/v1/commercial/pricing-rules?q=SC-&limit=500")
	rule := func(b J) {
		if _, ok := rules[b.S("code")]; ok {
			return // a rule is versioned: kept as seeded
		}
		b["effectiveFrom"], b["taxCodes"], b["pricingMode"] = eff, []string{"PPN"}, "plus_plus"
		admin.Post("/api/v1/commercial/pricing-rules", b)
		rules[b.S("code")] = b
	}
	court := func(code, item, dayCode, bandCode, price string) {
		b := J{"code": code, "name": fmt.Sprintf("%s %s %s", item, dayCode, bandCode), "serviceType": "sport_court", "itemRef": item, "unit": "slot",
			"unitMinutes": 60, "price": price, "revenueComponent": "court", "timeBandId": band[bandCode]}
		if dayCode != "" {
			b["lineDayTypeId"] = day[dayCode]
		}
		rule(b)
	}
	for _, r := range []struct{ item, short, monthuD, monthuE, friD, friE, satD, satE, sunD, sunE string }{
		{"FUTSAL-SG", "FSG", "175000", "245000", "195000", "275000", "185000", "215000", "185000", "215000"},
		{"FUTSAL-TF", "FTF", "185000", "265000", "220000", "315000", "195000", "238000", "195000", "238000"},
	} {
		for _, d := range []struct{ code, dayCode, dayPrice, evePrice string }{{"MT", "SC-MONTHU", r.monthuD, r.monthuE}, {"FR", "SC-FRI", r.friD, r.friE},
			{"SA", "SC-SAT", r.satD, r.satE}, {"SU", "SC-SUN", r.sunD, r.sunE}} {
			court("SC-"+r.short+"-"+d.code+"-D", r.item, d.dayCode, "SC-DAY16", d.dayPrice)
			court("SC-"+r.short+"-"+d.code+"-E", r.item, d.dayCode, "SC-EVE16", d.evePrice)
		}
	}
	court("SC-BV-D", "BASKET-VB", "", "SC-DAY17", "175000")
	court("SC-BV-E", "BASKET-VB", "", "SC-EVE17", "210000")
	court("SC-TEN-D", "TENNIS", "", "SC-DAY17", "200000")
	court("SC-TEN-E", "TENNIS", "", "SC-EVE17", "235000")
	// Basket Indoor: 1 hour Rp265.000, 2 hours Rp525.000 (the 2-hour rate applies from 2 hours)
	rule(J{"code": "SC-BI-1H", "name": "Basket Indoor 1 hour", "serviceType": "sport_court", "itemRef": "BASKET-IN", "unit": "slot", "unitMinutes": 60,
		"price": "265000", "revenueComponent": "court"})
	rule(J{"code": "SC-BI-2H", "name": "Basket Indoor 2 hours (Rp525.000)", "serviceType": "sport_court", "itemRef": "BASKET-IN", "unit": "slot", "unitMinutes": 60,
		"price": "262500", "minQuantity": 2, "minPolicy": "reject", "priority": 50, "revenueComponent": "court"})
	// the Sport Club entry (pool, gym, aerobic studio)
	for _, e := range []struct{ seg, wd, we string }{{"walk_in", "185000", "255000"}, {"guest_of_member", "145000", "210000"}, {"child", "95000", "135000"},
		{"family", "425000", "615000"}, {"staying_guest", "0", "0"}, {"corporate", "110000", "110000"}} {
		for _, d := range []struct{ code, day, price string }{{"WD", "SC-WEEKDAY", e.wd}, {"WE", "SC-WEEKEND", e.we}} {
			rule(J{"code": "SC-ENTRY-" + e.seg + "-" + d.code, "name": "Sport Club entry " + e.seg + " " + d.code, "serviceType": "facility_entry",
				"itemRef": trialSportEntryItem, "segment": e.seg, "lineDayTypeId": day[d.day], "unit": "entry", "price": d.price, "revenueComponent": "sport_entry"})
		}
	}
	// class registration: member / guest (a walk-in pays the guest fee)
	for _, c := range []struct{ item, name string }{{"SWIM-KIDS", "Swimming"}, {"TENNIS-CLINIC", "Tennis Lapangan"}, {"AIKIDO", "Aikido"}} {
		rule(J{"code": "SC-REG-" + c.item + "-M", "name": c.name + " registration (member)", "serviceType": "class_registration", "itemRef": c.item,
			"segment": "member", "unit": "registration", "price": "100000", "revenueComponent": "registration_fee"})
		rule(J{"code": "SC-REG-" + c.item + "-G", "name": c.name + " registration (guest)", "serviceType": "class_registration", "itemRef": c.item,
			"unit": "registration", "price": "200000", "priority": 50, "revenueComponent": "registration_fee"})
	}

	// ── facilities and courts (24 hours for two weeks, then the brochure hours) ──
	until := t.Today.AddDate(0, 0, 14).Format(time.DateOnly)
	hours := func(wd, we string) J {
		return J{"weekday": []string{wd[:5], wd[6:]}, "weekend": []string{we[:5], we[6:]}, "holiday": []string{we[:5], we[6:]}}
	}
	facs := byCode(sm, "/api/v1/sportclub/facilities?limit=50")
	for _, f := range []J{
		{"code": "POOL", "name": "Outdoor Swimming Pool", "facilityType": "swimming_pool", "usageMode": "entry", "capacity": 150, "priceItem": trialSportEntryItem,
			"openingHours": hours("06:00-21:00", "06:00-21:00")},
		{"code": "GYM", "name": "Gymnasium", "facilityType": "gym", "usageMode": "entry", "capacity": 40, "priceItem": trialSportEntryItem,
			"openingHours": hours("06:00-21:00", "06:00-21:00")},
		{"code": "AEROBIC", "name": "Aerobic Studio", "facilityType": "aerobic_studio", "usageMode": "entry", "capacity": 30, "priceItem": trialSportEntryItem,
			"openingHours": hours("06:00-21:00", "06:00-21:00")},
		{"code": "TENNIS", "name": "Tennis Arena", "facilityType": "tennis", "usageMode": "slot_booking", "priceItem": "TENNIS",
			"openingHours": hours("06:00-21:00", "06:00-21:00"), "attributes": J{"allDayUntil": until, "indoor": true, "description": "4 indoor courts under the dome"}},
		{"code": "FUTSAL", "name": "Futsal", "facilityType": "futsal", "usageMode": "slot_booking", "priceItem": "FUTSAL-SG",
			"openingHours": hours("07:00-21:00", "07:00-22:00"), "attributes": J{"allDayUntil": until, "description": "1 Taraflex and 2 synthetic grass courts"}},
		{"code": "BASKET-VB", "name": "Basket & Volley Arena", "facilityType": "basketball", "usageMode": "slot_booking", "priceItem": "BASKET-VB",
			"openingHours": hours("06:00-21:00", "06:00-21:00"), "attributes": J{"allDayUntil": until}},
		{"code": "BASKET-IN", "name": "Basket Indoor", "facilityType": "basketball", "usageMode": "slot_booking", "priceItem": "BASKET-IN",
			"openingHours": hours("06:00-21:00", "06:00-21:00"), "attributes": J{"allDayUntil": until, "indoor": true}},
	} {
		id := ensure(sm, "/api/v1/sportclub/facilities", facs, f)
		t.SetRef("facility:"+f.S("code"), id)
	}
	courts := byCode(sm, "/api/v1/sportclub/courts?limit=100")
	for _, c := range []J{
		{"code": "TENNIS-T1", "name": "Court 1 Tennis", "facility": "TENNIS", "surface": "Hard court", "indoor": true},
		{"code": "TENNIS-T2", "name": "Court 2 Tennis", "facility": "TENNIS", "surface": "Hard court", "indoor": true},
		{"code": "TENNIS-T3", "name": "Court 3 Tennis", "facility": "TENNIS", "surface": "Hard court", "indoor": true},
		{"code": "TENNIS-T4", "name": "Court 4 Tennis", "facility": "TENNIS", "surface": "Hard court", "indoor": true},
		{"code": "FUTSAL-TF", "name": "Futsal Taraflex", "facility": "FUTSAL", "surface": "Taraflex", "indoor": true, "priceItem": "FUTSAL-TF"},
		{"code": "FUTSAL-SG1", "name": "Futsal Synthetic Grass 1", "facility": "FUTSAL", "surface": "Synthetic grass", "priceItem": "FUTSAL-SG"},
		{"code": "FUTSAL-SG2", "name": "Futsal Synthetic Grass 2", "facility": "FUTSAL", "surface": "Synthetic grass", "priceItem": "FUTSAL-SG"},
		{"code": "BV-1", "name": "Basket & Volley Arena 1", "facility": "BASKET-VB", "surface": "Sport flooring"},
		{"code": "BV-2", "name": "Basket & Volley Arena 2", "facility": "BASKET-VB", "surface": "Sport flooring"},
		{"code": "BI-1", "name": "Basket Indoor 1", "facility": "BASKET-IN", "surface": "Parquet", "indoor": true},
		{"code": "BI-2", "name": "Basket Indoor 2", "facility": "BASKET-IN", "surface": "Parquet", "indoor": true},
	} {
		c["facilityId"] = t.Ref("facility:" + c.S("facility"))
		delete(c, "facility")
		ensure(sm, "/api/v1/sportclub/courts", courts, c)
	}

	// ── packages and vouchers ──
	vts := byCode(admin, "/api/v1/commercial/voucher-types?limit=300")
	voucher := func(v J) {
		t.SetRef("vouchertype:"+v.S("code"), ensure(admin, "/api/v1/commercial/voucher-types", vts, v))
	}
	voucher(J{"code": "ENTRY5-WD", "name": "5 Time Entry Voucher (Mon–Fri)", "kind": "quota", "category": "sport_entry", "unit": "entry", "faceValue": "5",
		"price": "635000", "applicableServices": []string{"facility_entry"}, "validityMonths": 3})
	voucher(J{"code": "ENTRY5-WE", "name": "5 Time Entry Voucher (weekend & holiday)", "kind": "quota", "category": "sport_entry", "unit": "entry", "faceValue": "5",
		"price": "950000", "applicableServices": []string{"facility_entry"}, "validityMonths": 3})
	for _, p := range []struct{ code, name, item, p4, p8 string }{
		{"FSG-WD-D", "Futsal Synthetic Mon–Fri 07.00–16.00", "FUTSAL-SG", "658000", "1245000"},
		{"FSG-WD-E", "Futsal Synthetic Mon–Fri 16.00–21.00", "FUTSAL-SG", "920000", "1740000"},
		{"FSG-WE-D", "Futsal Synthetic Sat–Sun 07.00–16.00", "FUTSAL-SG", "700000", "1325000"},
		{"FSG-WE-E", "Futsal Synthetic Sat–Sun 16.00–22.00", "FUTSAL-SG", "820000", "1550000"},
		{"FTF-WD-D", "Futsal Taraflex Mon–Fri 07.00–16.00", "FUTSAL-TF", "700000", "1325000"},
		{"FTF-WD-E", "Futsal Taraflex Mon–Fri 16.00–21.00", "FUTSAL-TF", "1000000", "1890000"},
		{"FTF-WE-D", "Futsal Taraflex Sat–Sun 07.00–16.00", "FUTSAL-TF", "735000", "1400000"},
		{"FTF-WE-E", "Futsal Taraflex Sat–Sun 16.00–22.00", "FUTSAL-TF", "900000", "1700000"},
		{"BV-D", "Basket & Volley 06.00–17.00", "BASKET-VB", "655000", "1245000"},
		{"BV-E", "Basket & Volley 17.00–21.00", "BASKET-VB", "795000", "1515000"},
	} {
		for _, n := range []struct{ x, price string }{{"4", p.p4}, {"8", p.p8}} {
			voucher(J{"code": p.code + "-" + n.x + "X", "name": p.name + " · " + n.x + "x", "kind": "quota", "category": "court_package", "unit": "use",
				"faceValue": n.x, "price": n.price, "applicableServices": []string{"sport_court"}, "applicableItems": []string{p.item}, "revenueComponent": "court",
				"validityMonths": 2})
		}
	}
	for _, c := range []struct{ item, short, name, m4, m8, g4, g8 string }{
		{"SWIM-KIDS", "SWIM", "Swimming", "465000", "795000", "535000", "957000"},
		{"TENNIS-CLINIC", "TENNIS", "Tennis Lapangan", "465000", "795000", "535000", "957000"},
		{"AIKIDO", "AIKIDO", "Aikido", "275000", "", "330000", ""},
	} {
		for _, v := range []struct{ seg, x, price string }{{"M", "4", c.m4}, {"M", "8", c.m8}, {"G", "4", c.g4}, {"G", "8", c.g8}} {
			if v.price == "" {
				continue
			}
			who := map[string]string{"M": "member", "G": "guest"}[v.seg]
			voucher(J{"code": c.short + "-" + v.seg + "-" + v.x + "X", "name": c.name + " " + v.x + "x (" + who + ")", "kind": "quota", "category": "class_package",
				"unit": "session", "faceValue": v.x, "price": v.price, "applicableItems": []string{c.item}, "revenueComponent": "class", "validityMonths": 2})
		}
	}

	// ── classes: Swimming, Tennis Lapangan, Aikido ──
	progs := byCode(sm, "/api/v1/sportclub/class-programs?limit=50")
	ensure(sm, "/api/v1/sportclub/class-programs", progs, J{"code": "SWIM-KIDS", "name": "Swimming", "discipline": "swimming", "capacity": 10, "durationMinutes": 60,
		"facilityId": t.Ref("facility:POOL"), "registrationValidMonths": 12})
	ensure(sm, "/api/v1/sportclub/class-programs", progs, J{"code": "TENNIS-CLINIC", "name": "Tennis Lapangan", "discipline": "tennis", "capacity": 8,
		"durationMinutes": 90, "registrationValidMonths": 12})
	aikido := ensure(sm, "/api/v1/sportclub/class-programs", progs, J{"code": "AIKIDO", "name": "Aikido", "discipline": "aikido", "capacity": 15,
		"durationMinutes": 90, "facilityId": t.Ref("facility:AEROBIC"), "registrationValidMonths": 12})
	ins := byCode(sm, "/api/v1/sportclub/instructors?limit=50")
	sensei := ensure(sm, "/api/v1/sportclub/instructors", ins, J{"code": "COACH-AIKIDO", "name": "Aikido Instructor", "disciplines": []string{"aikido"},
		"feeScheme": "per_session", "feeRate": "150000", "partnership": "partner"})
	if len(sm.Items("/api/v1/sportclub/class-schedules?filter[programId]="+aikido+"&limit=5")) == 0 {
		sm.Post("/api/v1/sportclub/class-schedules", J{"programId": aikido, "instructorId": sensei, "facilityId": t.Ref("facility:AEROBIC"),
			"weekdays": []int{2, 5}, "startTime": "19:00", "startDate": t.Today.AddDate(0, 0, -7).Format(time.DateOnly),
			"endDate": t.Today.AddDate(0, 0, t.Ahead).Format(time.DateOnly)})
	}

	// ── Sport Club membership (Type of Membership) ──
	pr := byCode(admin, "/api/v1/membership/programs?limit=50")
	prog := ensure(admin, "/api/v1/membership/programs", pr, J{"code": "SPORT", "name": "Sport Club Membership", "programKind": "sport_club", "operational": true,
		"description": "Sport Club of Modern Golf & Country Club: pool, gym, aerobic studio and member rates of the classes"})
	ent := J{"memberRate": true, "segment": "member", "facilityAccess": []string{"swimming_pool", "gym", "aerobic_studio"}, "freeEntry": true}
	types := byCode(admin, "/api/v1/membership/types?limit=100")
	packs := byCode(admin, "/api/v1/membership/packages?limit=200")
	for _, m := range []struct {
		code, name, category, fee, unit string
		family                          int
		elig                            J
	}{
		{"SC-IND", "Sport Club Individual", "individual", "8400000", "year", 0, J{}},
		{"SC-IND-RES", "Sport Club Individual Residence", "individual", "7350000", "year", 0, J{"residentOnly": true}},
		{"SC-COUPLE", "Sport Club Couple", "family", "12600000", "year", 1, J{"requireSpouse": true}},
		{"SC-FAM", "Sport Club Family (2+3)", "family", "18900000", "year", 4, J{"maxChildAge": 21, "maxChildren": 3}},
		{"SC-FAM-RES", "Sport Club Family Residence", "family", "15750000", "year", 4, J{"maxChildAge": 21, "maxChildren": 3, "residentOnly": true}},
		{"SC-SENIOR", "Sport Club Senior (above 60)", "individual", "5250000", "year", 0, J{"minAge": 60}},
		{"SC-STUDENT", "Sport Club Student (under 24)", "individual", "4725000", "year", 0, J{"maxAge": 24, "studentOnly": true}},
		{"SC-STUD-M", "Sport Club Monthly (Student)", "individual", "525000", "month", 0, J{"maxAge": 24, "studentOnly": true}},
		{"SC-CORP", "Sport Club Corporate", "corporate", "105000000", "year", 0, J{}},
	} {
		tid := ensure(admin, "/api/v1/membership/types", types, J{"code": m.code, "name": m.name, "programId": prog, "category": m.category,
			"maxFamilyMembers": m.family, "golfAccess": false, "eligibility": m.elig, "entitlements": ent})
		ensure(admin, "/api/v1/membership/packages", packs, J{"code": m.code + "-1", "name": m.name, "typeId": tid, "periodUnit": m.unit, "periodCount": 1,
			"joiningFee": "0", "periodFee": m.fee})
	}
}
