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

	"oneclub/internal/kernel/dbtx"
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

// demoHoles: par and distance in meters from the Black, Blue, White and Red
// tees of the course of Modern Golf & Country Club (hole cards of
// www.moderngolf.co.id, "Hole by Hole"); 6,311 m from the Black tees, par 72.
var demoHoles = [18][5]int{
	{5, 478, 458, 426, 407}, {4, 382, 360, 342, 324}, {3, 171, 127, 123, 115}, {4, 334, 319, 301, 271}, {5, 477, 454, 430, 410},
	{3, 165, 151, 140, 126}, {4, 405, 383, 369, 351}, {4, 378, 350, 322, 293}, {4, 331, 305, 288, 260},
	{4, 358, 339, 315, 293}, {3, 157, 139, 125, 109}, {4, 354, 328, 310, 272}, {4, 410, 384, 361, 334}, {5, 517, 484, 462, 417},
	{4, 340, 310, 290, 260}, {4, 364, 334, 324, 299}, {3, 190, 164, 149, 123}, {5, 500, 474, 445, 417},
}

var demoStrokeIndex = [18]int{15, 3, 9, 17, 7, 11, 1, 5, 13, 14, 6, 18, 2, 4, 16, 12, 10, 8}

// demoHoleGuide is the Hole-by-Hole text of each hole (www.moderngolf.co.id).
var demoHoleGuide = [18]string{
	"This is a good hole to start off with, an open par 5 with a wide fairway winding through mounds and across a small meandering stream at the second shot, ending in a small tightly bunkered green that sits amongst three connecting lakes. The stream will cause some decisions for golfers who have hit a poor tee shot, but there is still a chance left to make a par on this pleasant opening hole.",
	"Crossing the bridge and passing the palm-surrounded lakes of the first green brings you to the second tees. Here the longer hitters will be tested more severely than the average club player as they confront a necklace of bunkers awaiting a stray tee shot. A medium iron shot is the reward for accuracy, played to a small green guarded by a single pot bunker at the front.",
	"This one-shotter is a pretty picture indeed. Medium in length, the hazard and the beauty come in the form of a long lake stretching from the tees to the green, covered in flowering waterlilies. Play away from the lake too much and the shot will land in the greenside bunker. Accuracy pays on this, the first par 3.",
	"The tees are located beside an attractive little pond as the players contemplate a tee shot to a wide fairway which necks between bunkers and stream for the long hitters. A medium iron is played to a small green flanked by pot bunkers and mounds. It is important to make a good tee shot to set up the second to this pleasant little par 4.",
	"A strong par 5 which demands care to achieve length whilst avoiding the series of lakes along the left side of the fairway. The fairway is a mass of gentle undulations into which pot bunkers are placed at strategic places. Palms create a strong silhouette and finish to the green. Definitely a hole where caution is preferable to brute strength, and a very hard one to birdie.",
	"The tee shot needs to be correctly clubbed and struck if the bunkers and the hollows guarding the long green are to be successfully negotiated. A difficult birdie chance but a very pretty hole to play.",
	"Not only attractive but a very demanding par 4, requiring a daring tee shot staying close to the lake at the left to set up the shortest shot to the green. Swaying grasses and groundcovers hug the mounds and rough. All the hazards are between the golfer and the target, with the lure of open space awaiting the more cautious player to the right side. A good hole to escape with a par.",
	"Another strong par 4 but less awesome than the 7th. Although there is water to the left, it is a long way left and forms a pretty picture rather than a testing hazard. A series of mounds cross the front of the fairway around the shot length from the championship tee, inviting the golfer to let out the shaft and get past them.",
	"Back to the clubhouse with a medium-length two-shotter. The player needs to consider the tee shot with care, as a series of bunkers and mounds await a mishit or poorly directed shot. A large green awaits before the clubhouse, where the emphasis will be on putting on the gently undulating surface.",
	"Another opening hole with a wide, inviting fairway and danger only to the long hitters who stray to the right, where they will find bunkers. For the majority a medium-length second shot will find a large green guarded by a single pot bunker. Water flanks most of this hole, but it lies to the left, away from the line of play.",
	"The water this time is closer to the tees than to the target. A medium-length one-shotter: the tee shot needs to be properly clubbed and bravely played if a birdie is sought. A difficult par 3 with a testing putting surface; to miss this green is to invite an almost certain bogey.",
	"A strong par 4. Although there is water to the right, it forms a pretty picture rather than a testing hazard. A series of mounds cross the fairway around the shot length from the championship tee; get past them and the second to a well-bunkered green is greatly simplified.",
	"A strong par 4 with problems off the tees for all but the shorter hitters: mounds and hazards litter the landing area. Pass them safely and the long second is played to an angled green set among palms, open at the front but guarded at the sides. A very good hole to par and really tough to birdie.",
	"A long three-shotter which requires terrific accuracy and length to reach in two. Potted with bunkers and mounds covered with groundcovers and grasses, the target seems elusive in the distance; those who stray from the fairway into these bunkers usually go from bad to worse as the fairway winds its tortuous way to the green. A good hole to par.",
	"A shortish par 4, slightly dog-legged to the left and played to a long and narrow green. The secret is to play as close to the bunkers as prudence permits; this sets up the second to an open-fronted green bunkered only at the left by a deep pot. The green runs away to the right to a deep hollow. A good birdie chance for the bold.",
	"A longer par 4, wide open from the tee except for the long hitter, who must be straight or sorry. The plateau green is flanked by a deep hollow to the left and guarded by two pot bunkers at the front and right side, almost surrounding the green. A good hole to par.",
	"A medium-length one-shotter: the tee shot needs to be properly clubbed and bravely played if a birdie is sought. A difficult par 3 with a testing putting surface; take a longer club on this hole.",
	"With the driving range forming a huge water hazard at the left side, the temptation to play to the right is irresistible, and it is there that the bunkers lie in wait. A tough finish demanding accuracy and length if the little stream before the green is to be cleared to set up a birdie chance. A lovely finish to a grand round.",
}

func seedGolfDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var venue uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM platform.venues WHERE property_id = $1 AND code = 'GOLF'`, property).Scan(&venue); err != nil {
		return fmt.Errorf("golf venue: %w", err)
	}
	// ── course structure (EP-02) ──
	course := id.New()
	if err := tx.QueryRow(ctx, `INSERT INTO golf.courses (id, property_id, venue_id, code, name, holes, length_meters, par, description, guide)
		VALUES ($1,$2,$3,$4,'Modern Golf Championship Course',18,6311,72,
		'18-hole championship course of Modern Golf & Country Club, Tangerang.',
		'Mounds, pot bunkers and a chain of lakes on both nines. Night golf on the Front Nine under lights.')
		ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, course, property, venue, DemoCourseCode).Scan(&course); err != nil {
		return err
	}
	// Instances seeded before the real hole data carry a generic layout
	// (6,350 m, no Black tees): move them to the course of the club once.
	legacy := false
	if err := tx.QueryRow(ctx, `UPDATE golf.courses SET length_meters = 6311,
		guide = 'Mounds, pot bunkers and a chain of lakes on both nines. Night golf on the Front Nine under lights.'
		WHERE id = $1 AND length_meters = 6350 RETURNING true`, course).Scan(&legacy); err != nil && !dbtx.IsNoRows(err) {
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
	// Tee sets of the club's Handicap Index tables (slope per tee, course
	// rating = par). The scorecard picks the tee by gender, then "any", then
	// sequence: men play Blue, ladies Red, others White; Black is the
	// championship tee.
	type teeSet struct {
		code, name, color, gender string
		rating                    float64
		slope                     int
	}
	sets := []teeSet{{"BLUE", "Blue (Men)", "blue", "male", 72, 132}, {"WHITE", "White (Regular)", "white", "any", 72, 130},
		{"RED", "Red (Ladies)", "red", "female", 72, 132}, {"BLACK", "Black (Championship)", "black", "male", 72, 134}}
	for i, t := range sets {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tee_sets (id, property_id, course_id, code, name, color, course_rating, slope, gender, sequence)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (course_id, code) DO NOTHING`,
			id.New(), property, course, t.code, t.name, t.color, t.rating, t.slope, t.gender, i+1); err != nil {
			return err
		}
		if legacy {
			if _, err := tx.Exec(ctx, `UPDATE golf.tee_sets SET name = $3, course_rating = $4, slope = $5, sequence = $6 WHERE course_id = $1 AND code = $2`,
				course, t.code, t.name, t.rating, t.slope, i+1); err != nil {
				return err
			}
		}
	}
	for i, h := range demoHoles {
		n := i + 1
		sec := sections["FRONT"]
		if n > 9 {
			sec = sections["BACK"]
		}
		raw, _ := json.Marshal(map[string]int{"BLACK": h[1], "BLUE": h[2], "WHITE": h[3], "RED": h[4]})
		if _, err := tx.Exec(ctx, `INSERT INTO golf.holes (id, property_id, course_id, section_id, code, number, par, stroke_index, distances, description)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (course_id, number) DO NOTHING`,
			id.New(), property, course, sec, fmt.Sprintf("H%02d", n), n, h[0], demoStrokeIndex[i], raw, demoHoleGuide[i]); err != nil {
			return err
		}
		if legacy {
			if _, err := tx.Exec(ctx, `UPDATE golf.holes SET par = $3, stroke_index = $4, distances = $5, description = $6 WHERE course_id = $1 AND number = $2`,
				course, n, h[0], demoStrokeIndex[i], raw, demoHoleGuide[i]); err != nil {
				return err
			}
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
