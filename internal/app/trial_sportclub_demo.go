package app

// Sport Club court booking demo (docs/requirement-booking-sportclub-mgcc.md
// §13.9, FR-146..149): the website content of every sport (the photos of the
// club's Sport Club page), the Sport Club members of the demo access page of
// the Sport Club Member App (one persona per sport), two weeks of played
// bookings for the reports (every channel, no-shows, packages, extras) and
// the demo day relative to today: courts in play and not arrived yet,
// bookings to pay, a recurring community booking, a maintenance block, a
// package with quota left and the Sport Club staff on the roster.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/accounting"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/secret"
)

const sportPhoto = "/storage/app/uploads/public/"

// sportContent is the website content of the sports (FR-08, FR-77).
var sportContent = map[string]J{
	"TENNIS": {"sortOrder": 1, "onlineBooking": true, "bookingRules": J{"minHours": 1, "maxHours": 3, "eveningFrom": "17:00"},
		"content": J{"nameEn": "Tennis Arena", "slug": "tennis", "icon": "sports_tennis",
			"photos":      []string{sportPhoto + "630/49f/05e/63049f05eee9f777085623.jpg"},
			"description": J{"id": "4 lapangan tenis indoor di bawah dome, nyaman dimainkan siang maupun malam.", "en": "4 indoor tennis courts under the dome, comfortable day and night."},
			"rules": J{"id": []string{"Wajib sepatu dan pakaian olahraga", "Dilarang membawa makanan dari luar", "Pembayaran cashless", "Datang 15 menit sebelum jam main"},
				"en": []string{"Sport shoes and sportswear required", "No outside food", "Cashless payment", "Arrive 15 minutes before play"}},
			"amenities": []string{"Café", "Musholla", "Parkir", "Locker room"},
			"seoTitle":  J{"id": "Sewa Lapangan Tenis Indoor Tangerang — Modern Golf Sports Club", "en": "Indoor Tennis Court Rental Tangerang — Modern Golf Sports Club"}}},
	"FUTSAL": {"sortOrder": 2, "onlineBooking": true, "bookingRules": J{"minHours": 1, "maxHours": 4, "eveningFrom": "16:00"},
		"content": J{"nameEn": "Futsal", "slug": "futsal", "icon": "sports_soccer",
			"photos":      []string{sportPhoto + "630/49f/042/63049f0429c57345773415.jpg"},
			"description": J{"id": "1 lapangan futsal Taraflex dan 2 lapangan rumput sintetis. Harga berbeda per jenis lapangan.", "en": "1 Taraflex and 2 synthetic grass futsal courts. The rates differ per court type."},
			"rules": J{"id": []string{"Wajib sepatu futsal (bukan sepatu pul)", "Dilarang membawa makanan dari luar", "Pembayaran cashless", "Datang 15 menit sebelum jam main"},
				"en": []string{"Futsal shoes required (no studs)", "No outside food", "Cashless payment", "Arrive 15 minutes before play"}},
			"amenities": []string{"Café", "Musholla", "Parkir", "Locker room", "Rompi (sewa)"},
			"seoTitle":  J{"id": "Sewa Lapangan Futsal Taraflex & Sintetis Tangerang — Modern Golf Sports Club", "en": "Futsal Court Rental Tangerang — Modern Golf Sports Club"}}},
	"BASKET-VB": {"sortOrder": 3, "onlineBooking": true, "bookingRules": J{"minHours": 1, "maxHours": 4, "eveningFrom": "17:00"},
		"content": J{"nameEn": "Basket & Volley Arena", "slug": "basket-volley", "icon": "sports_volleyball",
			"photos":      []string{sportPhoto + "630/49f/008/63049f00859d7046295234.jpg", sportPhoto + "630/49f/036/63049f0366d23810418798.jpg"},
			"description": J{"id": "Arena basket dan voli dengan lantai olahraga, cocok untuk latihan tim dan komunitas.", "en": "Basketball and volleyball arena with sport flooring, for team training and communities."},
			"rules": J{"id": []string{"Wajib sepatu dan pakaian olahraga", "Dilarang membawa makanan dari luar", "Pembayaran cashless"},
				"en": []string{"Sport shoes and sportswear required", "No outside food", "Cashless payment"}},
			"amenities": []string{"Café", "Musholla", "Parkir", "Locker room"},
			"seoTitle":  J{"id": "Sewa Lapangan Basket & Voli Tangerang — Modern Golf Sports Club", "en": "Basketball & Volleyball Court Rental Tangerang — Modern Golf Sports Club"}}},
	"BASKET-IN": {"sortOrder": 4, "onlineBooking": true, "bookingRules": J{"minHours": 1, "maxHours": 2},
		"content": J{"nameEn": "Basket Indoor", "slug": "basket-indoor", "icon": "sports_basketball",
			"photos":      []string{sportPhoto + "630/49f/00c/63049f00c035b587766713.jpg"},
			"description": J{"id": "Lapangan basket indoor berlantai kayu. Harga per durasi: 1 jam Rp265.000, 2 jam Rp525.000.", "en": "Indoor parquet basketball court. Rate per duration: 1 hour Rp265,000, 2 hours Rp525,000."},
			"rules": J{"id": []string{"Wajib sepatu basket bersol non-marking", "Dilarang membawa makanan dari luar", "Pembayaran cashless"},
				"en": []string{"Non-marking basketball shoes required", "No outside food", "Cashless payment"}},
			"amenities": []string{"Café", "Musholla", "Parkir", "Locker room"},
			"seoTitle":  J{"id": "Sewa Lapangan Basket Indoor Tangerang — Modern Golf Sports Club", "en": "Indoor Basketball Court Rental Tangerang — Modern Golf Sports Club"}}},
	"POOL":    {"sortOrder": 10, "content": J{"nameEn": "Swimming Pool", "photos": []string{sportPhoto + "630/49f/06b/63049f06b2321932263495.jpg"}}},
	"GYM":     {"sortOrder": 11, "content": J{"nameEn": "Gymnasium", "photos": []string{sportPhoto + "630/49e/fd9/63049efd98f5b309130679.jpg"}}},
	"AEROBIC": {"sortOrder": 12, "content": J{"nameEn": "Aerobic Studio", "photos": []string{sportPhoto + "630/49e/fdd/63049efdda27c491016864.jpg"}}},
}

// sportCourtOrder is the order of the courts on the board and the website.
var sportCourtOrder = map[string]int{"TENNIS-T1": 1, "TENNIS-T2": 2, "TENNIS-T3": 3, "TENNIS-T4": 4, "FUTSAL-TF": 1, "FUTSAL-SG1": 2, "FUTSAL-SG2": 3,
	"BV-1": 1, "BV-2": 2, "BI-1": 1, "BI-2": 2}

// sportPersona is a Sport Club member of the demo access page.
type sportPersona struct {
	Email, Name, Phone, Code, Type, Sport string
}

// SportPersonas are the Sport Club members of the Sport Club Member App demo
// (sportmember domain /demo): one per sport, a family, a member with golf and
// Sport Club, and a guest without membership.
var SportPersonas = []sportPersona{
	{"sport.futsal@demo.oneclub.id", "Rizal Pratama", "+628111100001", "SC0001", "SC-IND", "FUTSAL"},
	{"sport.tennis@demo.oneclub.id", "Sinta Wijaya", "+628111100002", "SC0002", "SC-IND-RES", "TENNIS"},
	{"sport.basket@demo.oneclub.id", "Kevin Halim", "+628111100003", "SC0003", "SC-STUDENT", "BASKET-VB"},
	{"sport.swim@demo.oneclub.id", "Laras Putri", "+628111100004", "SC0004", "SC-IND", "POOL"},
	{"sport.family@demo.oneclub.id", "Budi Santoso", "+628111100005", "SC0005", "SC-FAM", "BASKET-IN"},
	{"sport.guest@demo.oneclub.id", "Andi Tamu", "+628111100006", "", "", "FUTSAL"},
}

func init() {
	for _, p := range SportPersonas {
		TrialUsers = append(TrialUsers, DemoUser{p.Email, p.Name, "member", "MAIN"})
	}
	TrialUsers = append(TrialUsers, DemoUser{"sport.court@demo.oneclub.id", "Bayu Petugas Lapangan", "sport_court_staff", "MAIN"})
}

// trialSportContent fills the website content and the order of the sports
// and courts.
func trialSportContent(t *Trial) {
	sm := t.As(trialSportManager)
	for _, f := range sm.Items("/api/v1/sportclub/facilities?limit=50") {
		if body, ok := sportContent[f.S("code")]; ok {
			sm.Patch("/api/v1/sportclub/facilities/"+f.S("id"), body)
		}
	}
	for _, c := range sm.Items("/api/v1/sportclub/courts?limit=100") {
		if o, ok := sportCourtOrder[c.S("code")]; ok {
			sm.Patch("/api/v1/sportclub/courts/"+c.S("id"), J{"sortOrder": o, "onlineBooking": true})
		}
	}
	// the gateway service fee posts to its revenue account (FR-93)
	ctx := dbtx.System(context.Background())
	t.check(t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := accounting.GenerateDefaultRules(ctx, tx)
		return err
	}))
}

// trialSportMembers links the persona users to Sport Club memberships and
// cards; the golf demo member gets a Sport Club membership too (two
// programs: the Golf | Sport Club switch, FR-109).
func trialSportMembers(ctx context.Context, t *Trial) {
	ctx = dbtx.System(ctx)
	t.check(t.ensureUsers(ctx)) // the personas and the court staff (idempotent)
	t.check(t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
		for _, p := range append(SportPersonas, sportPersona{"member@demo.oneclub.id", "Hendra Member", "+628111000001", DemoMemberCode, "SC-COUPLE", "TENNIS"}) {
			var userID uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM platform.users WHERE email = $1`, p.Email).Scan(&userID); err != nil {
				return fmt.Errorf("%s: %w", p.Email, err)
			}
			cust := id.New()
			if err := tx.QueryRow(ctx, `SELECT id FROM crm.customers WHERE user_id = $1 AND property_id = $2`, userID, t.Property).Scan(&cust); err != nil {
				if err := tx.QueryRow(ctx, `INSERT INTO crm.customers (id, property_id, code, name, email, phone, user_id, consent_at, consent_channel)
					VALUES ($1,$2,$3,$4,$5,$6,$7,now(),'back_office') ON CONFLICT (property_id, code) DO UPDATE SET user_id = EXCLUDED.user_id RETURNING id`,
					cust, t.Property, "CUS-"+strings.ToUpper(strings.Split(p.Email, "@")[0]), p.Name, p.Email, p.Phone, userID).Scan(&cust); err != nil {
					return err
				}
			}
			if p.Type == "" {
				continue // the guest: no membership
			}
			var member uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM membership.members WHERE property_id = $1 AND (code = $2 OR customer_id = $3) ORDER BY (code = $2) DESC LIMIT 1`,
				t.Property, p.Code, cust).Scan(&member); err != nil {
				member = id.New()
				if err := tx.QueryRow(ctx, `INSERT INTO membership.members (id, property_id, code, name, email, phone, membership_type, user_id, status, customer_id, joined_on)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'active',$9,billing.local_date($2) - 60) ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
					member, t.Property, p.Code, p.Name, p.Email, p.Phone, p.Type, userID, cust).Scan(&member); err != nil {
					return err
				}
			}
			var typeID, pkgID uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT t.id, k.id FROM membership.types t JOIN membership.packages k ON k.type_id = t.id WHERE t.code = $1 AND t.property_id = $2
				ORDER BY k.code LIMIT 1`, p.Type, t.Property).Scan(&typeID, &pkgID); err != nil {
				return fmt.Errorf("membership type %s: %w", p.Type, err)
			}
			var has bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.memberships WHERE member_id = $1 AND type_id = $2)`, member, typeID).Scan(&has); err != nil {
				return err
			}
			if has {
				continue
			}
			ms := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO membership.memberships (id, property_id, member_id, type_id, package_id, role, starts_on, ends_on, status, activated_at)
				VALUES ($1,$2,$3,$4,$5,'principal',billing.local_date($2) - 60,billing.local_date($2) + 305,'active',now())`, ms, t.Property, member, typeID, pkgID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO membership.cards (id, property_id, member_id, membership_id, card_number, card_type, qr_token, valid_until)
				VALUES ($1,$2,$3,$4,$5,'digital',$6,billing.local_date($2) + 305)`, id.New(), t.Property, member, ms, p.Code+"-SC", "mc_"+secret.RandomToken(24)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO billing.customer_accounts (id, property_id, number, customer_id, account_type, member_id, credit_limit)
				VALUES ($1,$2,$3,$4,'member',$5,10000000) ON CONFLICT DO NOTHING`, id.New(), t.Property, "MA-"+p.Code, cust, member); err != nil {
				return err
			}
		}
		// the Sport Club staff opens the Sport Club desk (HRIS work areas, FR-99)
		_, err := tx.Exec(ctx, `UPDATE hris.employees SET work_areas = '{sportclub}' WHERE property_id = $1 AND work_areas = '{}'
			AND org_unit_id IN (SELECT id FROM hris.org_units WHERE code = 'SPORT')`, t.Property)
		return err
	}))
}

type sportSlot struct {
	court string
	hour  int
	hours int
	who   int // persona index; -1 = walk-in guest
	via   string
	pay   string // cash, qris, card, member_account, package, online, member_app, unpaid
	show  bool   // checked in and played
}

// trialSportHistory books and plays two weeks of courts (FR-79..82 reports),
// at the simulated time of each booking.
func trialSportHistory(ctx context.Context, t *Trial) {
	var n int
	t.check(t.App.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM reservation.reservations WHERE property_id = $1 AND source_type = 'sportclub.court_booking'
			AND notes LIKE '[sc-history]%'`, t.Property).Scan(&n)
	}))
	// the court bookings of the leisure history were paid and played: close them (no "not arrived" left in the past)
	t.check(t.App.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines l SET status = 'completed' FROM reservation.reservations r
			WHERE r.id = l.reservation_id AND r.property_id = $1 AND r.source_type = 'sportclub.court_booking' AND l.status IN ('confirmed', 'checked_in')
			AND upper(l.period) < now() - interval '1 hour'`, t.Property); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE reservation.reservations r SET status = 'completed', completed_at = coalesce(completed_at, now())
			WHERE r.property_id = $1 AND r.source_type = 'sportclub.court_booking' AND r.status IN ('confirmed', 'checked_in')
			AND NOT EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND l.status IN ('held', 'confirmed', 'checked_in'))
			AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND l.status = 'completed')`, t.Property)
		return err
	}))
	if n > 0 {
		return
	}
	desk := t.As("sport.reception@demo.oneclub.id")
	courts := map[string]string{}
	for _, c := range desk.Items("/api/v1/sportclub/courts?limit=100") {
		courts[c.S("code")] = c.S("id")
	}
	guests := []string{"Komunitas Bola Cimone", "Dimas Saputra", "PT Sinar Jaya (Futsal Kantor)", "Agus Setiawan", "Tim Basket SMA 7", "Rina Kartika", "Yoga Pratama"}
	for d := 14; d >= 1; d-- {
		day := t.Today.AddDate(0, 0, -d)
		r := t.Rand(fmt.Sprintf("sc-hist:%d", d))
		var plan []sportSlot
		for _, c := range []string{"FUTSAL-TF", "FUTSAL-SG1", "FUTSAL-SG2", "TENNIS-T1", "TENNIS-T2", "TENNIS-T3", "BV-1", "BI-1"} {
			k := 1 + r.IntN(3)
			used := map[int]bool{}
			for i := 0; i < k; i++ {
				h := 7 + r.IntN(14)
				if c == "FUTSAL-TF" || c == "FUTSAL-SG1" || c == "FUTSAL-SG2" {
					h = 16 + r.IntN(5) // futsal plays in the evening
				}
				hrs := 1 + r.IntN(2)
				if used[h] || used[h+1] {
					continue
				}
				used[h], used[h+1] = true, true
				pays := []string{"cash", "qris", "card", "member_account", "online", "member_app", "cash"}
				pay := pays[r.IntN(len(pays))]
				who := -1
				if pay == "member_account" || pay == "member_app" {
					who = r.IntN(5)
				}
				via := "walk_in"
				if r.IntN(4) == 0 {
					via = "phone"
				}
				plan = append(plan, sportSlot{court: c, hour: h, hours: hrs, who: who, via: via, pay: pay, show: r.IntN(12) != 0})
			}
		}
		sort.Slice(plan, func(i, j int) bool { return plan[i].hour < plan[j].hour })
		type booked struct {
			id   string
			s    sportSlot
			sold bool
		}
		var done []booked
		for gi, s := range plan {
			t.At(day, fmt.Sprintf("%02d:10", s.hour-1))
			start := t.Clock(day, fmt.Sprintf("%02d:00", s.hour))
			lines := []J{{"courtId": courts[s.court], "start": start.Format(time.RFC3339), "end": start.Add(time.Duration(s.hours) * time.Hour).Format(time.RFC3339)}}
			var rid string
			switch s.pay {
			case "online":
				pub := t.Public()
				out, ok := pub.Try("POST", "/api/v1/public/court-bookings", J{"propertyId": t.Property.String(), "guest": J{"name": guests[gi%len(guests)],
					"phone": fmt.Sprintf("+62812%07d", 3000000+d*100+gi)}, "lines": lines, "payMethod": "qris", "terms": true})
				if !ok {
					continue
				}
				if num := out.M("checkout").M("online").S("number"); num != "" {
					pub.Try("POST", "/api/v1/public/sandbox-checkout/"+num+":complete", J{"outcome": "paid"})
				}
				t.check(t.flush(context.Background()))
				b, ok := pub.Try("GET", "/api/v1/public/court-bookings/"+out.S("token")+"?propertyId="+t.Property.String(), nil)
				if !ok || b.M("booking").S("payStatus") != "paid" {
					continue
				}
				// the desk finds it by its code
				if l := desk.Items("/api/v1/sportclub/bookings?q=" + out.S("reference")); len(l) > 0 {
					rid = l[0].S("id")
				}
			case "member_app":
				p := SportPersonas[s.who]
				out, ok := t.As(p.Email).Try("POST", "/api/v1/member/sport-club/court-bookings", J{"lines": lines, "memberCharge": true})
				if !ok {
					continue
				}
				rid = out.M("reservation").S("id")
			default:
				body := J{"lines": lines, "channel": "ops", "via": s.via, "notes": "[sc-history]"}
				if s.who >= 0 {
					body["customerId"] = t.sportCustomer(ctx, SportPersonas[s.who].Email)
				} else {
					body["guest"] = J{"name": guests[gi%len(guests)], "phone": fmt.Sprintf("+62813%07d", 4000000+d*100+gi)}
				}
				body["payment"] = J{"methodType": s.pay}
				out, ok := desk.Try("POST", "/api/v1/sportclub/bookings", body)
				if !ok {
					continue
				}
				rid = out.M("reservation").S("id")
			}
			if rid != "" {
				done = append(done, booked{id: rid, s: s})
			}
		}
		// play: check-in at the hour, extras sometimes, end of play; no-shows after the hour
		sort.Slice(done, func(i, j int) bool { return done[i].s.hour < done[j].s.hour })
		for i, b := range done {
			if !b.s.show {
				continue
			}
			t.At(day, fmt.Sprintf("%02d:05", b.s.hour))
			if _, ok := desk.Try("POST", "/api/v1/sportclub/bookings/"+b.id+":check-in", J{}); !ok {
				continue
			}
			if i%3 == 0 {
				if x, ok := desk.Try("POST", "/api/v1/sportclub/bookings/"+b.id+":extras", J{"items": []J{{"code": "WATER", "quantity": 4}}}); ok {
					if bal := x.M("booking").S("balance"); bal != "" && bal != "0" {
						desk.Try("POST", "/api/v1/sportclub/bookings/"+b.id+":pay", J{"methodType": "cash"})
					}
				}
			}
		}
		for _, b := range done {
			t.At(day, fmt.Sprintf("%02d:58", b.s.hour+b.s.hours-1))
			if b.s.show {
				desk.Try("POST", "/api/v1/sportclub/bookings/"+b.id+":complete", J{})
			} else {
				desk.Try("POST", "/api/v1/sportclub/bookings/"+b.id+":no-show", J{"reason": "tidak datang"})
			}
		}
	}
	t.Now()
}

// sportCustomer is the CRM customer of a persona.
func (t *Trial) sportCustomer(ctx context.Context, email string) string {
	if v := t.Ref("sc-cust:" + email); v != "" {
		return v
	}
	var cid string
	t.check(t.App.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT c.id::text FROM crm.customers c JOIN platform.users u ON u.id = c.user_id WHERE u.email = $1 LIMIT 1`, email).Scan(&cid)
	}))
	t.SetRef("sc-cust:"+email, cid)
	return cid
}

// trialSportDemoDay creates the court operation of the demo day at the real
// time (FR-147): in play, not arrived yet, to pay, members, a package, a
// recurring community booking and a maintenance block.
func trialSportDemoDay(ctx context.Context, t *Trial) {
	t.Now()
	now := time.Now().In(t.Loc)
	tag := "[sc-demo " + now.Format(time.DateOnly) + "]"
	var n int
	t.check(t.App.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM reservation.reservations WHERE property_id = $1 AND notes LIKE $2`, t.Property, tag+"%").Scan(&n)
	}))
	if n > 0 {
		return
	}
	desk := t.As("sport.reception@demo.oneclub.id")
	sm := t.As(trialSportManager)
	courts := map[string]string{}
	for _, c := range desk.Items("/api/v1/sportclub/courts?limit=100") {
		courts[c.S("code")] = c.S("id")
	}
	h0 := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, t.Loc)
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, t.Loc)
	book := func(court string, start time.Time, hours int, body J) string {
		body["lines"] = []J{{"courtId": courts[court], "start": start.Format(time.RFC3339), "end": start.Add(time.Duration(hours) * time.Hour).Format(time.RFC3339)}}
		if body["channel"] == nil {
			body["channel"] = "ops"
		}
		body["notes"] = tag
		out, ok := desk.Try("POST", "/api/v1/sportclub/bookings", body)
		if !ok {
			return ""
		}
		return out.M("reservation").S("id")
	}
	// in play now (checked in) and not arrived yet (paid, no check-in)
	if id := book("FUTSAL-TF", h0, 2, J{"guest": J{"name": "Komunitas Bola Cimone", "phone": "+6281299001122"}, "via": "walk_in",
		"payment": J{"methodType": "cash"}}); id != "" {
		desk.Try("POST", "/api/v1/sportclub/bookings/"+id+":check-in", J{})
		desk.Try("POST", "/api/v1/sportclub/bookings/"+id+":extras", J{"items": []J{{"code": "VEST", "quantity": 1}, {"code": "WATER", "quantity": 6}}})
	}
	book("TENNIS-T2", h0, 1, J{"customerId": t.sportCustomer(ctx, "sport.tennis@demo.oneclub.id"), "payment": J{"methodType": "member_account"}})
	// to pay at the desk (phone booking) and members later today / tomorrow
	book("FUTSAL-SG1", h0.Add(2*time.Hour), 2, J{"guest": J{"name": "Dimas Saputra", "phone": "+6281388002233"}, "via": "phone"})
	book("BV-1", h0.Add(3*time.Hour), 1, J{"customerId": t.sportCustomer(ctx, "sport.basket@demo.oneclub.id"), "payment": J{"methodType": "member_account"}})
	book("TENNIS-T1", tomorrow.Add(7*time.Hour), 2, J{"customerId": t.sportCustomer(ctx, "sport.tennis@demo.oneclub.id"), "payment": J{"methodType": "qris"}})
	book("BI-1", tomorrow.Add(19*time.Hour), 2, J{"customerId": t.sportCustomer(ctx, "sport.family@demo.oneclub.id"), "payment": J{"methodType": "card"}})
	// a court package with quota left (Rizal, futsal evenings)
	rizal := t.sportCustomer(ctx, "sport.futsal@demo.oneclub.id")
	pk := "FSG-WD-E-4X"
	if wd := tomorrow.Weekday(); wd == time.Saturday || wd == time.Sunday {
		pk = "FSG-WE-E-4X"
	}
	var code string
	if v, ok := desk.Try("POST", "/api/v1/commercial/vouchers:sell", J{"voucherTypeId": t.voucherTypeID(sm, pk), "customerId": rizal, "count": 1, "channel": "ops",
		"payment": J{"methodType": "cash"}}); ok {
		for _, x := range v.A("vouchers") {
			code = x.S("code")
		}
		if code == "" {
			code = v.S("code")
		}
	}
	if code != "" {
		book("FUTSAL-SG2", tomorrow.Add(20*time.Hour), 1, J{"customerId": rizal, "packageCode": code})
	}
	// a recurring community booking: Tuesday and Thursday 19:00, two months
	start := now
	sm.Try("POST", "/api/v1/sportclub/recurring", J{"courtId": courts["FUTSAL-SG1"], "weekdays": []int{2, 4}, "startTime": "19:00", "hours": 2,
		"startDate": start.AddDate(0, 0, 1).Format(time.DateOnly), "endDate": start.AddDate(0, 2, 0).Format(time.DateOnly),
		"guest": J{"name": "Komunitas Futsal Tangerang", "phone": "+6281277003344"}, "corporateName": "Komunitas Futsal Tangerang",
		"paymentMode": "pay_per_visit", "skipConflicts": true, "notes": tag})
	// maintenance of Court 4 tomorrow morning
	sm.Try("POST", "/api/v1/sportclub/blocks", J{"courtIds": []string{courts["TENNIS-T4"]}, "from": tomorrow.Format(time.DateOnly), "startTime": "06:00",
		"endTime": "08:00", "reason": "maintenance", "notes": "Perawatan permukaan lapangan " + tag})
	// a complaint handled at the desk
	desk.Try("POST", "/api/v1/sportclub/incidents", J{"category": "complaint", "severity": "low", "courtId": courts["TENNIS-T3"],
		"description": "Lampu lapangan 3 berkedip saat main malam " + tag, "actionTaken": "Teknisi diminta mengecek panel lampu"})
}

// voucherTypeID returns the id of a voucher type by code.
func (t *Trial) voucherTypeID(c *TrialClient, code string) string {
	if v := t.Ref("vouchertype:" + code); v != "" {
		return v
	}
	for _, x := range c.Items("/api/v1/commercial/voucher-types?limit=300") {
		if x.S("code") == code {
			t.SetRef("vouchertype:"+code, x.S("id"))
			return x.S("id")
		}
	}
	return ""
}
