package app

// Bungalows of Modern Golf as on the flyer "Fees & Rates" of the website
// (docs/requirement-booking-hotel-mgcc.md FR-H02, FR-H102–FR-H105,
// Lampiran A; decision of 10 Oct 2026: the flyer prices are right, the
// system follows them):
//
//   - room types Birdie, Eagle, Albatros and VIP Room with the flyer Room
//     Only rate as base rate and no weekend rate (a Saturday night costs
//     what a Monday night costs);
//   - 14 bungalows + 1 VIP Room. ASSUMPTION until the club confirms
//     (§13 no. 2): Birdie 6 · Eagle 5 · Albatros 3 · VIP 1, Birdie / Eagle /
//     VIP one bedroom for 2 adults, Albatros two bedrooms for 4 adults; the
//     views golf / lake / pool in turn; size, beds and location left empty;
//   - Stay rate plans Room Only (BAR, the default) and Long Stay (at least
//     7 nights, no breakfast, the flyer Long Stay price per type); Member and
//     Corporate rates stay, marked as assumptions; the made-up public prices
//     (Non-refundable Saver, seasons, the BNB package, promotions) are
//     archived;
//   - the Commercial rate card STAY_RO / STAY_LS with the flyer prices;
//   - add-ons (prices are assumptions, §13 no. 4) and preventive maintenance;
//   - the contact of the property (FR-H06) in the Stay Policies;
//   - the room attendants on the roster (FR-H83) and the front desk work
//     area "stay" (FR-H44);
//   - the demo day relative to today (FR-H103): arrivals (one unpaid, one
//     VIP), departures (one with a balance), in-house guests with F&B
//     charged to the room, a Dirty and an Out of Order bungalow, an open
//     guest request, a group, a corporate booking, a waitlist entry, a Long
//     Stay and upcoming stays.
//
// The master data runs in the leisure setup of a new dataset; on a dataset
// seeded before, the final step renames and completes the earlier data
// (stays keep the price snapshot they were booked with).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "bungalow-mgcc", Order: 42, Final: func(ctx context.Context, t *Trial) error {
		trialStayMGCC(t)
		trialStayPolicy(t)
		trialStayStaff(t)
		trialStayCatchUp(t)
		trialStayDemoDay(ctx, t)
		return nil
	}})
}

// trialStayType is a room type of the flyer.
type trialStayType struct {
	code, name, roomOnly, longStay string
	units, bedrooms, adults        int
	aliases                        []string // codes of the earlier seed
	desc, descEn                   string
}

var trialStayTypes = []trialStayType{
	{"BIRDIE", "Birdie Room", "605000", "550000", 6, 1, 2, nil,
		"Bungalow satu kamar tidur dengan ruang tamu dan AC, view lapangan golf, danau, atau kolam renang.",
		"One-bedroom bungalow with a living room and air conditioning, overlooking the golf course, the lake or the pool."},
	{"EAGLE", "Eagle Room", "770000", "715000", 5, 1, 2, nil,
		"Bungalow satu kamar tidur yang lebih lega dengan ruang tamu dan AC, view lapangan golf, danau, atau kolam renang.",
		"A roomier one-bedroom bungalow with a living room and air conditioning, overlooking the golf course, the lake or the pool."},
	{"ALBATROS", "Albatros Room", "1045000", "935000", 3, 2, 4, []string{"ALBATROSS"},
		"Bungalow dua kamar tidur dengan ruang tamu dan AC untuk keluarga atau rombongan kecil.",
		"Two-bedroom bungalow with a living room and air conditioning for a family or a small group."},
	{"VIP", "VIP Room", "1265000", "1155000", 1, 1, 2, nil,
		"VIP Room untuk bermalam dengan ruang tamu dan AC.",
		"The VIP Room for an overnight stay, with a living room and air conditioning."},
}

// trialStayPhotos are the bungalow photos of the website.
var trialStayPhotos = []string{
	"/storage/app/uploads/public/630/4a0/4ee/6304a04eeacb2982274352.jpg",
	"/storage/app/uploads/public/630/4a0/4f1/6304a04f17087491010200.jpg",
	"/storage/app/uploads/public/630/4a0/515/6304a05150ef7566735951.jpg",
	"/storage/app/uploads/public/630/4a0/518/6304a05187f70121420479.jpg",
	"/storage/app/uploads/public/630/4a0/524/6304a0524de64246290792.jpg",
}

// trialStayTry is a request of the stay seed that may fail (the data was
// changed by hand, a unit is taken); the failure is logged, not fatal.
func trialStayTry(t *Trial, c *TrialClient, method, path string, body any) (J, bool) {
	st, out, raw := c.Call(method, path, body)
	if st < 200 || st > 299 {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		t.logf("bungalow-mgcc: %s %s → %d %s", method, path, st, msg)
		return out, false
	}
	return out, true
}

// trialStayMGCC creates or corrects the bungalow master data (idempotent).
func trialStayMGCC(t *Trial) {
	admin, rm := t.Admin(), t.As(trialResort)
	byCode := func(c *TrialClient, path string) map[string]J {
		out := map[string]J{}
		for _, x := range c.Items(path) {
			out[strings.ToUpper(x.S("code"))] = x
		}
		return out
	}
	// ensure creates the record or patches the fields of the one seeded before
	ensure := func(c *TrialClient, path string, have map[string]J, body J, aliases ...string) string {
		x, ok := have[body.S("code")]
		for _, a := range aliases {
			if !ok {
				x, ok = have[a]
			}
		}
		if ok {
			trialStayTry(t, c, "PATCH", path+"/"+x.S("id"), body)
			have[body.S("code")] = x
			return x.S("id")
		}
		id := c.Post(path, body).S("id")
		have[body.S("code")] = J{"id": id, "code": body.S("code")}
		return id
	}
	// archive removes a record from sale; one already used is set Inactive
	archive := func(c *TrialClient, path string, have map[string]J, codes ...string) {
		for _, code := range codes {
			x, ok := have[code]
			if !ok {
				continue
			}
			if st, _, _ := c.Call("DELETE", path+"/"+x.S("id"), nil); st >= 300 && x.S("status") != "inactive" {
				trialStayTry(t, c, "PATCH", path+"/"+x.S("id"), J{"status": "inactive"})
			}
		}
	}

	// ── room types and units ──
	types := byCode(rm, "/api/v1/stay/bungalow-types?limit=50")
	units := byCode(rm, "/api/v1/stay/bungalows?limit=200")
	amenities := []J{
		{"group": "general", "icon": "ac", "label": "AC", "labelEn": "Air conditioning"},
		{"group": "general", "icon": "sofa", "label": "Ruang tamu", "labelEn": "Living room"},
	}
	views := []string{"golf", "lake", "pool"}
	n := 0
	for i, ty := range trialStayTypes {
		photos := []string{trialStayPhotos[i%len(trialStayPhotos)], trialStayPhotos[(i+1)%len(trialStayPhotos)], trialStayPhotos[(i+2)%len(trialStayPhotos)]}
		tid := ensure(rm, "/api/v1/stay/bungalow-types", types, J{"code": ty.code, "name": ty.name, "nameEn": ty.name, "maxAdults": ty.adults,
			"maxChildren": 2, "bedrooms": ty.bedrooms, "baseRate": ty.roomOnly, "weekendRate": nil, "bedConfiguration": nil, "sizeSqm": nil,
			"view": "Lapangan golf, danau, atau kolam renang", "facilities": []string{"ac", "living_room"}, "amenities": amenities,
			"description": ty.desc + " (Jumlah unit dan kapasitas: asumsi demo, menunggu konfirmasi klub.)", "descriptionEn": ty.descEn,
			"slug": strings.ToLower(ty.code), "photos": photos, "onWebsite": true, "sortOrder": i + 1, "priceItem": ty.code, "status": "active"}, ty.aliases...)
		t.SetRef("bungalowtype:"+ty.code, tid)
		for u := 1; u <= ty.units; u++ {
			code := fmt.Sprintf("%s-%02d", ty.code[:1], u)
			ensure(rm, "/api/v1/stay/bungalows", units, J{"code": code, "name": fmt.Sprintf("%s %02d", ty.name, u), "typeId": tid,
				"view": views[n%len(views)], "location": nil, "status": "active"})
			n++
		}
	}

	// ── Stay rate plans ──
	pool := []string{"swimming_pool", "gym"}
	plans := byCode(rm, "/api/v1/stay/rate-plans?limit=50")
	ensure(rm, "/api/v1/stay/rate-plans", plans, J{"code": "BAR", "name": "Room Only", "planType": "standard", "derivation": "bar", "isDefault": true,
		"includesBreakfast": false, "minNights": 1, "freeCancelHours": 48, "cancelFeePercent": "50", "noShowFeePercent": "100", "facilityAccess": pool,
		"description": "Harga kamar sesuai flyer (Room Only).", "sortOrder": 1, "status": "active"})
	longStay := ensure(rm, "/api/v1/stay/rate-plans", plans, J{"code": "LONGSTAY", "name": "Long Stay", "planType": "standard", "derivation": "fixed",
		"includesBreakfast": false, "minNights": 7, "freeCancelHours": 48, "cancelFeePercent": "50", "noShowFeePercent": "100", "facilityAccess": pool,
		"description": "Minimal 7 malam, tanpa sarapan (non breakfast), harga per malam sesuai flyer.", "sortOrder": 2, "status": "active"})
	ensure(rm, "/api/v1/stay/rate-plans", plans, J{"code": "MEMBER", "name": "Member Rate", "planType": "member", "derivation": "percent", "adjustValue": "-15",
		"eligibility": "member", "includesBreakfast": true, "freeCancelHours": 24, "facilityAccess": []string{"*"}, "sortOrder": 3,
		"description": "Asumsi demo (belum dikonfirmasi klub): 15% di bawah Room Only + sarapan, khusus member.", "status": "active"})
	ensure(rm, "/api/v1/stay/rate-plans", plans, J{"code": "CORP", "name": "Corporate Rate", "planType": "corporate", "derivation": "percent", "adjustValue": "-12",
		"eligibility": "corporate", "paymentPolicy": "pay_at_hotel", "facilityAccess": pool, "sortOrder": 4,
		"description": "Asumsi demo (belum dikonfirmasi klub): 12% di bawah Room Only, bayar di hotel, khusus korporat.", "status": "active"})
	archive(rm, "/api/v1/stay/rate-plans", plans, "NONREF")
	prices := map[string]J{}
	for _, p := range rm.Items("/api/v1/stay/rate-plan-prices?limit=200") {
		if p.S("ratePlanId") == longStay {
			prices[p.S("bungalowTypeId")] = p
		}
	}
	for _, ty := range trialStayTypes {
		tid := t.Ref("bungalowtype:" + ty.code)
		if p, ok := prices[tid]; ok {
			trialStayTry(t, rm, "PATCH", "/api/v1/stay/rate-plan-prices/"+p.S("id"), J{"weekdayPrice": ty.longStay, "weekendPrice": nil})
			continue
		}
		rm.Post("/api/v1/stay/rate-plan-prices", J{"ratePlanId": longStay, "bungalowTypeId": tid, "weekdayPrice": ty.longStay})
	}
	// made-up public prices: archived
	archive(rm, "/api/v1/stay/seasons", byCode(rm, "/api/v1/stay/seasons?limit=50"), "HOLIDAY", "PEAK")
	archive(rm, "/api/v1/stay/packages", byCode(rm, "/api/v1/stay/packages?limit=50"), "BNB")
	archive(rm, "/api/v1/stay/promotions", byCode(rm, "/api/v1/stay/promotions?limit=50"), "STAY3PAY2", "EARLYBIRD")

	// ── add-ons (prices: assumption) and preventive maintenance ──
	addons := byCode(rm, "/api/v1/stay/addons?limit=50")
	for _, a := range []struct{ code, name, cat, price, unit, avail string }{
		{"BREAKFAST", "Sarapan", "breakfast", "125000", "per_person_night", "both"},
		{"EXTRA-BED", "Extra Bed", "extra_bed", "350000", "per_night", "both"},
		{"BBQ", "BBQ Dinner Set", "bbq", "450000", "per_item", "both"},
		{"AIRPORT", "Antar-jemput Bandara", "transportation", "300000", "per_item", "both"},
		{"LAUNDRY", "Laundry (per kantong)", "laundry", "75000", "per_item", "in_stay"},
		{"TOWEL", "Handuk Tambahan", "extra_towel", "25000", "per_item", "in_stay"},
	} {
		id := ensure(rm, "/api/v1/stay/addons", addons, J{"code": a.code, "name": a.name, "category": a.cat, "price": a.price, "unit": a.unit,
			"availability": a.avail, "pricingMode": "nett", "description": "Harga asumsi demo, menunggu konfirmasi klub. Opsional, tidak mengubah harga kamar."})
		t.SetRef("addon:"+a.code, id)
	}
	pm := byCode(rm, "/api/v1/stay/preventive-schedules?limit=50")
	for _, p := range []J{
		{"code": "AC-CLEAN", "name": "Cuci AC", "category": "ac", "intervalDays": 90, "nextDue": t.Today.AddDate(0, 0, 3).Format(time.DateOnly),
			"assignedTo": "Teknisi Resort"},
		{"code": "WATER-HEATER", "name": "Pemeriksaan water heater", "category": "water_heater", "intervalDays": 180,
			"nextDue": t.Today.AddDate(0, 0, 10).Format(time.DateOnly), "assignedTo": "Teknisi Resort"},
	} {
		if _, ok := pm[p.S("code")]; !ok {
			rm.Post("/api/v1/stay/preventive-schedules", p)
		}
	}

	// ── Commercial rate card (sidebar of the website): Room Only and Long Stay ──
	eff := t.Start.AddDate(0, 0, -30).Format(time.DateOnly)
	cplans := byCode(admin, "/api/v1/commercial/rate-plans?limit=200")
	ro := ensure(admin, "/api/v1/commercial/rate-plans", cplans, J{"code": "STAY_RO", "name": "Room Only", "serviceType": "bungalow", "minNights": 1,
		"facilityAccess": pool})
	ls := ensure(admin, "/api/v1/commercial/rate-plans", cplans, J{"code": "STAY_LS", "name": "Long Stay (min. 7 malam)", "serviceType": "bungalow",
		"minNights": 7, "facilityAccess": pool})
	rules := map[string]J{}
	for _, r := range admin.Items("/api/v1/commercial/pricing-rules?limit=500&q=-RO") {
		rules[r.S("code")] = r
	}
	for _, r := range admin.Items("/api/v1/commercial/pricing-rules?limit=500&q=BGL-") {
		rules[r.S("code")] = r
	}
	for _, old := range []string{"EAGLE-RO", "ALBATROSS-RO"} { // the made-up prices of the earlier seed
		if r, ok := rules[old]; ok && r.S("status") == "active" {
			trialStayTry(t, admin, "PATCH", "/api/v1/commercial/pricing-rules/"+r.S("id"), J{"status": "inactive"})
		}
	}
	for _, ty := range trialStayTypes {
		for _, p := range []struct{ suffix, plan, label, price string }{{"RO", ro, "Room Only", ty.roomOnly}, {"LS", ls, "Long Stay", ty.longStay}} {
			code := "BGL-" + ty.code + "-" + p.suffix
			if _, ok := rules[code]; ok {
				continue // a rule is versioned: kept as seeded
			}
			admin.Post("/api/v1/commercial/pricing-rules", J{"code": code, "name": ty.name + " " + p.label, "serviceType": "bungalow", "itemRef": ty.code,
				"ratePlanId": p.plan, "unit": "night", "price": p.price, "revenueComponent": "bungalow", "effectiveFrom": eff, "taxCodes": []string{"PPN"},
				"pricingMode": "nett"})
		}
	}
}

// trialStayPolicy fills the contact of the property and the booking terms
// of the Stay Policies (FR-H06, deploy step 2), keeping what was set (the
// Golf Manager holds the club policies).
func trialStayPolicy(t *Trial) {
	admin := t.As(trialGolfManager)
	value := J{}
	for _, e := range admin.Items("/api/v1/platform/club-policies/catalog") {
		if e.S("code") == "stay.policy" {
			if raw, err := json.Marshal(e["inForce"]); err == nil && string(raw) != "null" {
				_ = json.Unmarshal(raw, &value)
			}
		}
	}
	want := J{"contactAddress": "Jl. Modern Golf Raya No 99, Perumahan Kota Modern, Tangerang 15117", "contactPhone": "(021) 552 9228",
		"contactEmail": "marketing@modernland.co.id", "contactWhatsApp": "+62 811 925 2277",
		"mapUrl":             "https://maps.google.com/?q=Modern+Golf+%26+Country+Club+Tangerang",
		"childPolicy":        "Anak di bawah 6 tahun menginap gratis tanpa extra bed (asumsi demo, menunggu konfirmasi klub).",
		"houseRulesEn":       "Check-in from 14:00, check-out until 12:00. No smoking inside the bungalow. Quiet hours 22:00-06:00. Pets are not allowed.",
		"websiteHoldMinutes": 15, "bookingWindowDays": 365, "onlineMethods": "qris,virtual_account,card", "keysPerBungalow": 2}
	changed := false
	for k, v := range want {
		if fmt.Sprint(value[k]) != fmt.Sprint(v) {
			value[k], changed = v, true
		}
	}
	if changed {
		trialStayTry(t, admin, "POST", "/api/v1/platform/club-policies", J{"category": "Stay Policies", "code": "stay.policy",
			"name": "Check-in / check-out & deposit", "propertyId": t.Property.String(), "value": value})
	}
}

// trialStayStaff puts the room attendants on the roster of the next two
// weeks (housekeeping tasks go to the one on duty) and opens the Stay Front
// Desk for the front office staff.
func trialStayStaff(t *Trial) {
	hr := t.As(trialHR)
	unit := t.ids("orgunit", "/api/v1/hris/org-units?limit=500", trialHR)("BUNGALOW")
	if unit == "" {
		t.logf("bungalow-mgcc: org unit BUNGALOW not found; roster skipped")
		return
	}
	positions := map[string]J{}
	for _, p := range hr.Items("/api/v1/hris/positions?limit=500") {
		positions[p.S("code")] = p
	}
	for code, role := range map[string]string{"FO-AGENT": "front_office", "FO-MGR": "front_office"} {
		if p, ok := positions[code]; ok && p.S("workforceRole") == "" {
			trialStayTry(t, hr, "PATCH", "/api/v1/hris/positions/"+p.S("id"), J{"workforceRole": role})
		}
	}
	ra := positions["BGL-RA"].S("id")
	if ra == "" {
		ra = hr.Post("/api/v1/hris/positions", J{"code": "BGL-RA", "name": "Room Attendant", "orgUnitId": unit, "headcount": 2,
			"workforceRole": "housekeeping"}).S("id")
	}
	var attendants []string
	for _, e := range hr.Items("/api/v1/hris/employees?limit=500&orgUnitId=" + unit) {
		if e.S("orgUnitId") != unit {
			continue
		}
		if e.S("positionId") == ra {
			attendants = append(attendants, e.S("id"))
		}
		if fmt.Sprint(e["workAreas"]) == "[]" {
			trialStayTry(t, hr, "PATCH", "/api/v1/hris/employees/"+e.S("id"), J{"workAreas": []string{"stay"}})
		}
	}
	if len(attendants) == 0 {
		for _, n := range []struct{ name, gender, phone string }{{"Sari Wulandari", "female", "+6281277100201"}, {"Wati Rahmawati", "female", "+6281277100202"}} {
			if e, ok := trialStayTry(t, hr, "POST", "/api/v1/hris/employees", J{"fullName": n.name, "gender": n.gender, "phone": n.phone, "orgUnitId": unit,
				"positionId": ra, "jobTitle": "Room Attendant", "joinDate": t.Start.AddDate(0, -6, 0).Format(time.DateOnly), "employmentStatus": "contract",
				"workerCategory": "regular", "status": "active", "workAreas": []string{"stay"}}); ok {
				attendants = append(attendants, e.S("id"))
			}
		}
	}
	if len(attendants) == 0 {
		return
	}
	templates := map[string]J{}
	for _, s := range hr.Items("/api/v1/hris/shift-templates?limit=200") {
		templates[s.S("code")] = s
	}
	shifts := map[string]string{}
	for _, s := range []J{
		{"code": "HK-AM", "name": "Housekeeping Pagi", "startTime": "07:00", "endTime": "15:00", "breakMinutes": 60},
		{"code": "HK-PM", "name": "Housekeeping Siang", "startTime": "12:00", "endTime": "20:00", "breakMinutes": 60},
	} {
		if x, ok := templates[s.S("code")]; ok {
			shifts[s.S("code")] = x.S("id")
			continue
		}
		s["orgUnitId"], s["workforceRole"] = unit, "housekeeping"
		shifts[s.S("code")] = hr.Post("/api/v1/hris/shift-templates", s).S("id")
	}
	now := time.Now().In(t.Loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.Loc)
	to := from.AddDate(0, 0, 13)
	const name = "Roster Housekeeping Bungalow"
	var sch J
	for _, s := range hr.Items("/api/v1/hris/schedules?limit=100&orgUnitId=" + unit + "&from=" + from.Format(time.DateOnly) + "&to=" + to.Format(time.DateOnly)) {
		if s.S("name") == name && s.S("status") != "cancelled" {
			sch = s
		}
	}
	if sch.S("status") == "published" {
		return
	}
	if sch == nil {
		var ok bool
		if sch, ok = trialStayTry(t, hr, "POST", "/api/v1/hris/schedules", J{"orgUnitId": unit, "periodStart": from.Format(time.DateOnly),
			"periodEnd": to.Format(time.DateOnly), "name": name, "notes": "Room attendant bungalow (seed demo)"}); !ok {
			return
		}
	}
	var as []J
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		for i, e := range attendants {
			code := []string{"HK-AM", "HK-PM"}[(i+d.Day())%2]
			if wd := (int(d.Weekday()) + 3*i) % 7; wd == 5 || wd == 6 { // two rest days a week, never both attendants off together
				as = append(as, J{"employeeId": e, "workDate": d.Format(time.DateOnly), "off": true})
				continue
			}
			as = append(as, J{"employeeId": e, "workDate": d.Format(time.DateOnly), "shiftTemplateId": shifts[code]})
		}
	}
	trialStayTry(t, hr, "POST", "/api/v1/hris/schedules/"+sch.S("id")+":assign", J{"assignments": as})
	trialStayTry(t, hr, "POST", "/api/v1/hris/schedules/"+sch.S("id")+":publish", J{})
}

// trialStayCatchUp brings the stays of a dataset seeded some days ago to
// today: guests whose stay ended check out on their departure day, the
// reservations that arrived since are checked in on their arrival day.
func trialStayCatchUp(t *Trial) {
	rm, cashier := t.As(trialResort), t.As(trialCashier)
	now := time.Now().In(t.Loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.Loc)
	day := func(s string) time.Time {
		at, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return today
		}
		at = at.In(t.Loc)
		return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, t.Loc)
	}
	r := t.Rand("stay-catch-up")
	for _, s := range rm.Items("/api/v1/stay/stays?filter[kind]=bungalow&filter[status]=reserved&limit=200") {
		arr := day(s.S("start"))
		if !arr.Before(today) {
			continue
		}
		t.At(arr, "14:30")
		trialStayTry(t, rm, "POST", "/api/v1/stay/stays/"+s.S("id")+":check-in", J{"idType": "ktp", "nationality": "ID", "keysIssued": 2,
			"idNumber": fmt.Sprintf("3671%012d", r.IntN(1000000000000)), "overrideStatus": true, "supervisorReason": "Data demo"})
	}
	for _, s := range rm.Items("/api/v1/stay/stays?filter[kind]=bungalow&filter[status]=checked_in&limit=200") {
		dep := day(s.S("end"))
		if !dep.Before(today) {
			continue
		}
		t.At(dep, "11:00")
		trialPayFolio(t, cashier, s.S("folioId"), []string{"card", "bank_transfer"}[r.IntN(2)])
		trialStayTry(t, rm, "POST", "/api/v1/stay/stays/"+s.S("id")+":check-out", J{"at": t.Clock(dep, "11:30").Format(time.RFC3339), "keysReturned": 2,
			"overrideKeys": true, "supervisorReason": "Data demo"})
	}
	t.Now()
}

// trialStayDemoDay creates the front desk day of the demo at the real date
// (FR-H103); it runs once per date.
func trialStayDemoDay(ctx context.Context, t *Trial) {
	t.Now()
	now := time.Now().In(t.Loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.Loc)
	tag := "[stay-demo " + today.Format(time.DateOnly) + "]"
	var n int
	t.check(t.App.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM stay.stays WHERE property_id = $1 AND notes LIKE $2`, t.Property, tag+"%").Scan(&n)
	}))
	if n > 0 {
		return
	}
	rm, cashier := t.As(trialResort), t.As(trialCashier)
	typ := func(code string) string { return t.Ref("bungalowtype:" + code) }
	ds := func(d time.Time) string { return d.Format(time.DateOnly) }
	unitByCode := map[string]string{}
	for _, b := range rm.Items("/api/v1/stay/bungalows?limit=200") {
		unitByCode[b.S("code")] = b.S("id")
	}
	r := t.Rand("stay-demo:" + ds(today))
	book := func(body J) J {
		body["kind"] = "bungalow"
		if body["notes"] == nil {
			body["notes"] = tag
		} else {
			body["notes"] = tag + " " + body.S("notes")
		}
		if body["adults"] == nil {
			body["adults"] = 2
		}
		out, ok := trialStayTry(t, rm, "POST", "/api/v1/stay/stays", body)
		if !ok {
			return nil
		}
		return out.M("stay")
	}
	checkIn := func(s J, unit string) {
		if s == nil {
			return
		}
		body := J{"idType": "ktp", "idNumber": fmt.Sprintf("3671%012d", r.IntN(1000000000000)), "nationality": "ID", "keysIssued": 2,
			"overrideStatus": true, "supervisorReason": "Data demo"}
		if unit != "" {
			body["unitId"] = unitByCode[unit]
			if _, ok := trialStayTry(t, rm, "POST", "/api/v1/stay/stays/"+s.S("id")+":check-in", body); ok {
				return
			}
			delete(body, "unitId") // the unit is taken: any free one
		}
		trialStayTry(t, rm, "POST", "/api/v1/stay/stays/"+s.S("id")+":check-in", body)
	}
	charge := func(s J, cat, desc, price string) {
		if s != nil {
			trialStayTry(t, rm, "POST", "/api/v1/stay/stays/"+s.S("id")+":charge", J{"category": cat, "description": desc, "unitPrice": price, "quantity": 1})
		}
	}
	dp := func(ref string) J { return J{"methodType": "bank_transfer", "reference": ref} }
	breakfast := []J{{"addonId": t.Ref("addon:BREAKFAST"), "quantity": 1}}

	// departures today: arrived two days ago (one settled, one with an F&B balance)
	t.At(today.AddDate(0, 0, -2), "14:00")
	dep1 := book(J{"bungalowTypeId": typ("EAGLE"), "arrivalDate": ds(today.AddDate(0, 0, -2)), "departureDate": ds(today), "bookingSource": "website",
		"guest": J{"name": "Rudi Hartono", "phone": "+6281311220011", "email": "rudi.hartono@example.com"}, "payment": dp("WEB-DEP1")})
	checkIn(dep1, "E-01")
	dep2 := book(J{"bungalowTypeId": typ("BIRDIE"), "arrivalDate": ds(today.AddDate(0, 0, -2)), "departureDate": ds(today), "bookingSource": "phone",
		"guest": J{"name": "Lina Marlina", "phone": "+6281311220012"}, "payment": dp("TRF-DEP2"), "addons": breakfast})
	checkIn(dep2, "B-01")
	// a Long Stay in-house (7 nights, arrived three days ago)
	t.At(today.AddDate(0, 0, -3), "15:00")
	long := book(J{"bungalowTypeId": typ("ALBATROS"), "arrivalDate": ds(today.AddDate(0, 0, -3)), "departureDate": ds(today.AddDate(0, 0, 4)),
		"ratePlan": "LONGSTAY", "adults": 3, "bookingSource": "phone", "guest": J{"name": "Keluarga Tanoto", "phone": "+6281311220013"},
		"payment": dp("TRF-LS"), "notes": "Long Stay 7 malam"})
	checkIn(long, "A-01")
	// in-house guests (arrived yesterday) with F&B charged to the room
	t.At(today.AddDate(0, 0, -1), "14:30")
	in1 := book(J{"bungalowTypeId": typ("BIRDIE"), "arrivalDate": ds(today.AddDate(0, 0, -1)), "departureDate": ds(today.AddDate(0, 0, 2)),
		"bookingSource": "front_desk", "guest": J{"name": "Yohanes Pratama", "phone": "+6281311220014"}, "payment": dp("TRF-IH1")})
	checkIn(in1, "B-02")
	in2 := book(J{"bungalowTypeId": typ("EAGLE"), "arrivalDate": ds(today.AddDate(0, 0, -1)), "departureDate": ds(today.AddDate(0, 0, 1)),
		"bookingSource": "website", "guest": J{"name": "Maria Gunawan", "phone": "+6281311220015", "email": "maria.g@example.com"}, "payment": dp("WEB-IH2"),
		"addons": breakfast})
	checkIn(in2, "E-02")
	t.At(today.AddDate(0, 0, -1), "20:15")
	charge(in1, "fnb", "The Spike Bar Restaurant — makan malam (2 pax)", "385000")
	charge(in2, "fnb", "The Spike Bar Restaurant — room service", "245000")
	charge(dep2, "fnb", "The Spike Bar Restaurant — makan malam", "310000")
	charge(long, "laundry", "Laundry 2 kantong", "150000")
	t.Now()
	if dep1 != nil {
		trialPayFolio(t, cashier, dep1.S("folioId"), "card")
	}
	// an open guest request
	if in1 != nil {
		trialStayTry(t, rm, "POST", "/api/v1/stay/guest-requests", J{"stayId": in1.S("id"), "requestType": "extra_towel", "quantity": 2, "source": "phone",
			"description": "Minta 2 handuk tambahan sebelum jam 17.00"})
	}

	// arrivals today: one paid, one unpaid (phone), one VIP
	book(J{"bungalowTypeId": typ("BIRDIE"), "arrivalDate": ds(today), "departureDate": ds(today.AddDate(0, 0, 2)), "bookingSource": "website",
		"guest": J{"name": "Andreas Wijaya", "phone": "+6281311220016", "email": "andreas.w@example.com"}, "payment": dp("WEB-ARR1"),
		"expectedArrival": "15:00"})
	book(J{"bungalowTypeId": typ("EAGLE"), "arrivalDate": ds(today), "departureDate": ds(today.AddDate(0, 0, 1)), "bookingSource": "phone",
		"guest": J{"name": "Dewi Anggraini", "phone": "+6281311220017"}, "notes": "Belum bayar — bayar saat check-in", "expectedArrival": "18:00"})
	vipGuest := J{"name": "Bapak Hendra Kusuma", "phone": "+6281311220018", "email": "hendra.k@example.com"}
	vip := book(J{"bungalowTypeId": typ("VIP"), "arrivalDate": ds(today), "departureDate": ds(today.AddDate(0, 0, 2)), "bookingSource": "phone", "vip": true,
		"guest": vipGuest, "payment": J{"methodType": "card", "reference": "EDC-VIP"}, "addons": breakfast, "expectedArrival": "16:00",
		"specialRequests": "Welcome fruit dan air mineral ekstra"})
	if vip != nil {
		trialStayTry(t, rm, "POST", "/api/v1/stay/handover-notes", J{"category": "vip", "stayId": vip.S("id"),
			"body": "Tamu VIP tiba ±16.00 di VIP Room. Siapkan welcome fruit; GM ingin menyapa."})
	}
	// waitlist: the VIP Room is taken tonight and tomorrow
	trialStayTry(t, rm, "POST", "/api/v1/stay/waitlist", J{"bungalowTypeId": typ("VIP"), "arrivalDate": ds(today.AddDate(0, 0, 1)), "nights": 1, "adults": 2,
		"guest": J{"name": "Ibu Ratna Sari", "phone": "+6281311220019"}, "notes": tag + " Ingin VIP Room, bersedia Albatros bila penuh"})

	// a Dirty and an Out of Order bungalow
	if id := unitByCode["B-06"]; id != "" {
		trialStayTry(t, rm, "POST", "/api/v1/stay/bungalows/"+id+":room-status", J{"status": "dirty", "reason": "Tamu check-out pagi ini, belum dibersihkan"})
	}
	if id := unitByCode["A-03"]; id != "" {
		trialStayTry(t, rm, "POST", "/api/v1/stay/room-blocks", J{"bungalowId": id, "kind": "out_of_order", "reason": "AC bocor, menunggu teknisi " + tag,
			"startDate": ds(today), "endDate": ds(today.AddDate(0, 0, 2))})
	}

	// a group (golf outing) and a corporate booking
	trialStayTry(t, rm, "POST", "/api/v1/stay/groups", J{"name": "Golf Outing PT Sinar Jaya", "arrivalDate": ds(today.AddDate(0, 0, 3)),
		"departureDate": ds(today.AddDate(0, 0, 5)), "bookingSource": "corporate", "folioMode": "combined",
		"guest":   J{"name": "PT Sinar Jaya (Bpk. Arif)", "phone": "+6281311220020", "email": "arif@sinarjaya.example.com"},
		"items":   []J{{"bungalowTypeId": typ("BIRDIE"), "occupantName": "Arif Setiawan"}, {"bungalowTypeId": typ("BIRDIE"), "occupantName": "Budi Santoso"}, {"bungalowTypeId": typ("EAGLE"), "occupantName": "Chandra Wijaya"}},
		"payment": dp("TRF-GRP"), "notes": tag + " Rombongan golf outing, 3 bungalow"})
	corp := ""
	for _, c := range t.Admin().Items("/api/v1/crm/corporate-accounts?limit=20") {
		if corp == "" && c.S("status") != "inactive" {
			corp = c.S("id")
		}
	}
	if corp != "" {
		book(J{"bungalowTypeId": typ("EAGLE"), "arrivalDate": ds(today.AddDate(0, 0, 1)), "departureDate": ds(today.AddDate(0, 0, 3)),
			"bookingSource": "corporate", "corporateAccountId": corp, "ratePlan": "CORP", "guest": J{"name": "Fajar Nugroho", "phone": "+6281311220021"},
			"notes": "Tamu korporat"})
	}

	// upcoming stays: Room Only (some with breakfast) and Long Stay
	guests := t.Guests()
	codes := []string{"BIRDIE", "BIRDIE", "EAGLE", "EAGLE", "ALBATROS"}
	for d := 4; d < 30; d += 3 {
		g := guests[r.IntN(len(guests))]
		arr := today.AddDate(0, 0, d)
		nights := 1 + r.IntN(3)
		body := J{"bungalowTypeId": typ(codes[r.IntN(len(codes))]), "arrivalDate": ds(arr), "departureDate": ds(arr.AddDate(0, 0, nights)),
			"customerId": g.ID, "bookingSource": []string{"website", "phone", "front_desk"}[r.IntN(3)], "payment": dp("DP-" + arr.Format("0102"))}
		if d%9 == 1 {
			body["ratePlan"], body["departureDate"] = "LONGSTAY", ds(arr.AddDate(0, 0, 7))
		} else if r.IntN(2) == 0 {
			body["addons"] = breakfast
		}
		book(body)
	}
}
