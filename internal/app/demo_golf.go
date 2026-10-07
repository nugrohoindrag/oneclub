package app

// Demo data of the P1 Golf Core MVP for Modern Golf & Country Club (PRD P1
// FR-CRS-08, FR-TEE-01, FR-PRC-03 rate card effective 1 April 2026). It is
// idempotent: every insert is skipped when the row exists.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/secret"
)

// DemoCourseCode is the seeded championship course.
const DemoCourseCode = "MGC"

// DemoMemberCode is the member number of the demo member.
const DemoMemberCode = "D0001"

// P1DemoUsers are the staff of the golf operation (one per P1 role).
var P1DemoUsers = []DemoUser{
	{"golf.admin@demo.oneclub.id", "Fajar Golf Admin", "golf_admin", "MAIN"},
	{"caddy.master@demo.oneclub.id", "Yanti Caddy Master", "caddy_manager", "MAIN"},
	{"front.desk@demo.oneclub.id", "Rudi Front Desk", "front_desk", "MAIN"},
	{"reservation@demo.oneclub.id", "Lina Reservation", "reservation_staff", "MAIN"},
	{"golf.staff@demo.oneclub.id", "Bayu Golf Staff", "golf_staff", "MAIN"},
	{"membership@demo.oneclub.id", "Maya Membership Manager", "membership_manager", "MAIN"},
	{"membership.admin@demo.oneclub.id", "Tono Membership Admin", "membership_admin", "MAIN"},
	{"caddy@demo.oneclub.id", "Siti Caddy", "caddy", "MAIN"}, // Caddy Tablet, linked to caddy C001
}

// holes: par and championship (BLUE) distance in meters; 6,350 m, par 72.
var demoHoles = [18][2]int{
	{4, 360}, {4, 370}, {3, 170}, {5, 510}, {4, 380}, {4, 350}, {3, 165}, {5, 520}, {4, 375},
	{4, 365}, {5, 505}, {3, 180}, {4, 370}, {4, 355}, {5, 515}, {3, 160}, {4, 380}, {4, 320},
}

var demoStrokeIndex = [18]int{7, 3, 15, 1, 5, 11, 17, 9, 13, 8, 2, 16, 4, 12, 6, 18, 10, 14}

func seedGolfDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var venue uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM platform.venues WHERE property_id = $1 AND code = 'GOLF'`, property).Scan(&venue); err != nil {
		return fmt.Errorf("golf venue: %w", err)
	}
	// ── course structure (EP-02) ──
	course := id.New()
	if err := tx.QueryRow(ctx, `INSERT INTO golf.courses (id, property_id, venue_id, code, name, holes, length_meters, par, description, guide)
		VALUES ($1,$2,$3,$4,'Modern Golf Championship Course',18,6350,72,
		'18-hole championship course of Modern Golf & Country Club, Tangerang.',
		'Tree-lined fairways with water on the back nine. Night golf on the Front Nine under lights.')
		ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, course, property, venue, DemoCourseCode).Scan(&course); err != nil {
		return err
	}
	sections := map[string]uuid.UUID{}
	for i, s := range [][2]string{{"FRONT", "Front Nine"}, {"BACK", "Back Nine"}} {
		sid := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO golf.course_sections (id, property_id, course_id, code, name, sequence) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (course_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, sid, property, course, s[0], s[1], i+1).Scan(&sid); err != nil {
			return err
		}
		sections[s[0]] = sid
	}
	type teeSet struct {
		code, name, color, gender string
		rating                    float64
		slope                     int
		factor                    float64
	}
	sets := []teeSet{{"BLUE", "Blue (Championship)", "blue", "male", 72.5, 131, 1}, {"WHITE", "White (Regular)", "white", "any", 70.6, 125, 0.93},
		{"RED", "Red (Ladies)", "red", "female", 71.8, 123, 0.82}}
	for i, t := range sets {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tee_sets (id, property_id, course_id, code, name, color, course_rating, slope, gender, sequence)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (course_id, code) DO NOTHING`,
			id.New(), property, course, t.code, t.name, t.color, t.rating, t.slope, t.gender, i+1); err != nil {
			return err
		}
	}
	for i, h := range demoHoles {
		n := i + 1
		sec := sections["FRONT"]
		if n > 9 {
			sec = sections["BACK"]
		}
		dist := map[string]int{}
		for _, t := range sets {
			dist[t.code] = int(float64(h[1])*t.factor + 0.5)
		}
		raw, _ := json.Marshal(dist)
		if _, err := tx.Exec(ctx, `INSERT INTO golf.holes (id, property_id, course_id, section_id, code, number, par, stroke_index, distances)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (course_id, number) DO NOTHING`,
			id.New(), property, course, sec, fmt.Sprintf("H%02d", n), n, h[0], demoStrokeIndex[i], raw); err != nil {
			return err
		}
	}
	routes := map[string]uuid.UUID{}
	for _, r := range []struct {
		code, name, sections string
		holes                int
		def                  bool
	}{{"CH18", "Championship 18", "FRONT,BACK", 18, true}, {"FRONT9", "Front Nine", "FRONT", 9, false}, {"BACK9", "Back Nine", "BACK", 9, false}} {
		rid := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO golf.playing_routes (id, property_id, course_id, code, name, section_codes, hole_count, is_default)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (course_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
			rid, property, course, r.code, r.name, r.sections, r.holes, r.def).Scan(&rid); err != nil {
			return err
		}
		routes[r.code] = rid
	}

	// ── day types, time bands, templates (EP-03) ──
	dayTypes := map[string]uuid.UUID{}
	for _, d := range []struct {
		code, name, weekdays string
		holidays             bool
	}{{"WEEKDAY", "Weekday (Mon–Fri)", "1,2,3,4,5", false}, {"WEEKEND", "Weekend & Public Holiday", "6,7", true}} {
		did := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO commercial.day_types (id, property_id, code, name, weekdays, includes_holidays) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, did, property, d.code, d.name, d.weekdays, d.holidays).Scan(&did); err != nil {
			return err
		}
		dayTypes[d.code] = did
	}
	bands := map[string]uuid.UUID{}
	for _, b := range [][5]string{{"AM", "Morning", "morning", "05:00", "11:00"}, {"PM", "Afternoon", "afternoon", "11:00", "16:00"}, {"NIGHT", "Night", "night", "16:00", "22:00"}} {
		bid := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO commercial.time_bands (id, property_id, code, name, session, start_time, end_time) VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, bid, property, b[0], b[1], b[2], b[3], b[4]).Scan(&bid); err != nil {
			return err
		}
		bands[b[0]] = bid
	}
	for _, t := range []struct {
		code, name, day, session, start, end, tees, route string
		interval, min                                     int
		peak, lighting                                    bool
	}{
		{"WD-AM", "Weekday Morning", "WEEKDAY", "morning", "05:30", "08:10", "1,10", "CH18", 8, 2, false, false},
		{"WD-PM", "Weekday Afternoon", "WEEKDAY", "afternoon", "11:20", "14:00", "1,10", "CH18", 8, 2, false, false},
		{"WD-NIGHT", "Weekday Night Golf", "WEEKDAY", "night", "16:30", "20:00", "1", "FRONT9", 8, 3, false, true},
		{"WE-AM", "Weekend & PH Morning", "WEEKEND", "morning", "05:37", "08:11", "1,10", "CH18", 7, 2, true, false},
		{"WE-PM", "Weekend & PH Afternoon", "WEEKEND", "afternoon", "12:02", "14:00", "1,10", "CH18", 7, 2, false, false},
		{"WE-NIGHT", "Weekend & PH Night Golf", "WEEKEND", "night", "16:30", "20:00", "1", "FRONT9", 8, 3, false, true},
	} {
		route := routes[t.route]
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tee_sheet_templates (id, property_id, course_id, code, name, day_type_code, session, start_time, end_time,
			interval_minutes, start_tees, flights_per_slot, min_players, max_players, playing_route_id, peak, lighting, effective_from)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,1,$12,4,$13,$14,$15,'2026-01-01') ON CONFLICT (course_id, code) DO NOTHING`,
			id.New(), property, course, t.code, t.name, t.day, t.session, t.start, t.end, t.interval, t.tees, t.min, route, t.peak, t.lighting); err != nil {
			return err
		}
	}

	// ── tax and rate card (EP-08) ──
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.tax_service_rules (id, property_id, code, name, kind, rate_percent, basis, pricing_mode, effective_from)
		VALUES ($1,$2,'PPN','PPN 11%','tax',11,'net_amount','nett','2026-01-01') ON CONFLICT (property_id, code, effective_from) DO NOTHING`, id.New(), property); err != nil {
		return err
	}
	plan := id.New()
	if err := tx.QueryRow(ctx, `INSERT INTO commercial.rate_plans (id, property_id, code, name, description, pricing_mode, effective_from)
		VALUES ($1,$2,'RC2026','Golf Rate Card 2026','All-in rates incl. green fee, caddy fee, buggy fee, HIO, mineral water and PPN 11%','nett','2026-04-01')
		ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, plan, property).Scan(&plan); err != nil {
		return err
	}
	allIn, _ := json.Marshal([]map[string]any{
		{"code": "green_fee", "name": "Green Fee", "type": "remainder"},
		{"code": "caddy_fee", "name": "Caddy Fee", "type": "amount", "value": "150000", "liability": true},
		{"code": "buggy_fee", "name": "Buggy Fee", "type": "amount", "value": "125000"},
		{"code": "hio", "name": "Hole-in-One Insurance", "type": "amount", "value": "10000"},
		{"code": "water", "name": "Mineral Water", "type": "amount", "value": "5000"},
	})
	type rule struct {
		code, name, charge, segment, day, band string
		price                                  int64
		priority                               int
		comps                                  []byte
	}
	rules := []rule{
		{"MEMBER", "Member — every day", "golf_round", "member", "", "", 640000, 100, allIn},
		{"GUEST-WD", "Guest — Mon–Fri", "golf_round", "guest", "WEEKDAY", "", 995000, 100, allIn},
		{"GUEST-WE-AM", "Guest — Sat–Sun/PH Morning", "golf_round", "guest", "WEEKEND", "AM", 2960000, 100, allIn},
		{"GUEST-WE-PM", "Guest — Sat–Sun/PH Afternoon", "golf_round", "guest", "WEEKEND", "PM", 1960000, 100, allIn},
		{"GUEST-NIGHT", "Guest — Night Golf", "golf_round", "guest", "", "NIGHT", 1050000, 90, allIn},
		{"SENIOR-WD", "Senior — Mon–Fri", "golf_round", "senior", "WEEKDAY", "", 740000, 100, allIn},
		{"LADIES-WD", "Ladies — Mon–Fri", "golf_round", "ladies", "WEEKDAY", "", 740000, 100, allIn},
		{"JUNIOR-WD", "Junior — Mon–Fri", "golf_round", "junior", "WEEKDAY", "", 740000, 100, allIn},
		{"EXTRA-CART", "Additional buggy (non-member)", "extra_cart", "", "", "", 560000, 100, []byte("[]")},
		{"EXTRA-CART-MBR", "Additional buggy (member)", "extra_cart", "member", "", "", 0, 100, []byte("[]")},
	}
	for _, r := range rules {
		var seg *string
		if r.segment != "" {
			seg = &r.segment
		}
		var day, band *uuid.UUID
		if r.day != "" {
			d := dayTypes[r.day]
			day = &d
		}
		if r.band != "" {
			b := bands[r.band]
			band = &b
		}
		// the all-in rate card includes PPN 11% only (the club's other Tax &
		// Service rules belong to F&B, banquet … and must not split it)
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.pricing_rules (id, property_id, rate_plan_id, code, name, charge_type, segment, day_type_id, time_band_id,
			price, components, priority, tax_codes, effective_from) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'{PPN}','2026-04-01')
			ON CONFLICT (property_id, code, version) DO NOTHING`,
			id.New(), property, plan, r.code, r.name, r.charge, seg, day, band, r.price, r.comps, r.priority); err != nil {
			return err
		}
	}
	// ── membership (EP-06) ──
	prog := id.New()
	if err := tx.QueryRow(ctx, `INSERT INTO membership.programs (id, property_id, code, name, program_kind, operational, description)
		VALUES ($1,$2,'GOLF','Golf Membership','golf',true,'Golf membership of Modern Golf & Country Club')
		ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, prog, property).Scan(&prog); err != nil {
		return err
	}
	types := map[string]uuid.UUID{}
	for _, t := range []struct {
		code, name, category, elig string
		family, nominees           int
	}{
		{"IND", "Golf Individual", "individual", `{"minAge":18}`, 0, 0},
		{"FAM", "Golf Family", "family", `{"minAge":18,"maxChildAge":21,"maxChildren":3,"requireSpouse":false}`, 4, 0},
		{"CORP", "Golf Corporate", "corporate", `{}`, 0, 2},
	} {
		tid := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO membership.types (id, property_id, program_id, code, name, category, max_family_members, max_nominees, eligibility)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
			tid, property, prog, t.code, t.name, t.category, t.family, t.nominees, t.elig).Scan(&tid); err != nil {
			return err
		}
		types[t.code] = tid
	}
	packages := map[string]uuid.UUID{}
	for _, p := range []struct {
		code, name, typ string
		joining, fee    int64
	}{{"IND-1Y", "Individual — 1 Year", "IND", 50000000, 12000000}, {"FAM-1Y", "Family — 1 Year", "FAM", 75000000, 18000000},
		{"CORP-1Y", "Corporate — 1 Year", "CORP", 150000000, 36000000}} {
		pid := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO membership.packages (id, property_id, type_id, code, name, period_unit, period_count, joining_fee, period_fee)
			VALUES ($1,$2,$3,$4,$5,'year',1,$6,$7) ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
			pid, property, types[p.typ], p.code, p.name, p.joining, p.fee).Scan(&pid); err != nil {
			return err
		}
		packages[p.code] = pid
	}

	// ── caddies, golf carts, lockers (EP-09, EP-10, EP-11) ──
	caddyNames := []string{"Siti", "Ani", "Rina", "Dewi", "Lestari", "Wulan", "Putri", "Yuni", "Ratna", "Sri",
		"Intan", "Mega", "Nur", "Fitri", "Eka", "Tuti", "Ayu", "Indah", "Nia", "Dian"}
	for i, n := range caddyNames {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.caddies (id, property_id, code, name, gender) VALUES ($1,$2,$3,$4,'female')
			ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, fmt.Sprintf("C%03d", i+1), n); err != nil {
			return err
		}
	}
	// caddy@demo.oneclub.id signs in to the Caddy Tablet as C001 (My Assignments)
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_profiles AS p (caddy_id, property_id, user_id)
		SELECT c.id, c.property_id, u.id FROM golf.caddies c, platform.users u
		WHERE c.property_id = $1 AND c.code = 'C001' AND u.email = 'caddy@demo.oneclub.id'
		ON CONFLICT (caddy_id) DO UPDATE SET user_id = EXCLUDED.user_id WHERE p.user_id IS NULL`, property); err != nil {
		return err
	}
	for i := 1; i <= 30; i++ {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.golf_carts (id, property_id, code, name) VALUES ($1,$2,$3,$4)
			ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, fmt.Sprintf("B%02d", i), fmt.Sprintf("Buggy %02d", i)); err != nil {
			return err
		}
	}
	for _, l := range []struct {
		prefix, area string
		n            int
	}{{"M", "male", 40}, {"F", "female", 20}} {
		for i := 1; i <= l.n; i++ {
			if _, err := tx.Exec(ctx, `INSERT INTO golf.lockers (id, property_id, code, name, area) VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, fmt.Sprintf("%s%03d", l.prefix, i),
				fmt.Sprintf("Locker %s%03d", l.prefix, i), l.area); err != nil {
				return err
			}
		}
	}

	// ── demo member linked to member@demo.oneclub.id (Member Portal) ──
	var userID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM platform.users WHERE email = 'member@demo.oneclub.id'`).Scan(&userID); err != nil {
		return err
	}
	cust := id.New()
	if err := tx.QueryRow(ctx, `INSERT INTO crm.customers (id, property_id, code, name, email, phone, gender, birth_date, user_id, consent_at, consent_channel)
		VALUES ($1,$2,'CUS-DEMO-001','Hendra Member','member@demo.oneclub.id','+628111000001','male','1975-05-17',$3,now(),'back_office')
		ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, cust, property, userID).Scan(&cust); err != nil {
		return err
	}
	member := id.New()
	if err := tx.QueryRow(ctx, `INSERT INTO membership.members (id, property_id, code, name, email, phone, membership_type, user_id, status, customer_id, joined_on)
		VALUES ($1,$2,$3,'Hendra Member','member@demo.oneclub.id','+628111000001','IND',$4,'active',$5,billing.local_date($2) - 30)
		ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, member, property, DemoMemberCode, userID, cust).Scan(&member); err != nil {
		return err
	}
	var hasMembership bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.memberships WHERE member_id = $1)`, member).Scan(&hasMembership); err != nil {
		return err
	}
	if !hasMembership {
		ms := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO membership.memberships (id, property_id, member_id, type_id, package_id, role, starts_on, ends_on, status, activated_at)
			VALUES ($1,$2,$3,$4,$5,'principal',billing.local_date($2) - 30,billing.local_date($2) + 335,'active',now())`, ms, property, member, types["IND"], packages["IND-1Y"]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO membership.cards (id, property_id, member_id, membership_id, card_number, card_type, qr_token, valid_until)
			VALUES ($1,$2,$3,$4,$5,'digital',$6,billing.local_date($2) + 335)`, id.New(), property, member, ms, DemoMemberCode+"-01", "mc_"+secret.RandomToken(24)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO membership.history (id, property_id, member_id, membership_id, event, to_status, details)
			VALUES ($1,$2,$3,$4,'activated','active','{"source":"demo"}')`, id.New(), property, member, ms); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.customer_accounts (id, property_id, number, customer_id, account_type, member_id, credit_limit)
		VALUES ($1,$2,'MA-DEMO-0001',$3,'member',$4,25000000) ON CONFLICT (property_id, number) DO NOTHING`, id.New(), property, cust, member); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.handicaps (id, property_id, customer_id, handicap_index, source)
		SELECT $1,$2,$3,14.2,'manual' WHERE NOT EXISTS (SELECT 1 FROM golf.handicaps WHERE customer_id = $3)`, id.New(), property, cust); err != nil {
		return err
	}

	// ── public holidays 2026 (platform calendar) ──
	for _, h := range [][2]string{{"2026-12-25", "Hari Raya Natal"}, {"2026-08-17", "Hari Kemerdekaan RI"}, {"2027-01-01", "Tahun Baru Masehi"}} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.calendar_days (id, property_id, day, name, kind) VALUES ($1,NULL,$2,$3,'public_holiday')
			ON CONFLICT DO NOTHING`, id.New(), h[0], h[1]); err != nil {
			return err
		}
	}
	return nil
}
