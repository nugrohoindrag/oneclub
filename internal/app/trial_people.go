package app

// Trial dataset: the club's people. At go-live (the day before the
// history) the existing customers, corporate accounts and members of the
// club are migrated from Rhapsody with the migration tool itself
// (internal/app/rhapsody: stage → validate → load), as a club switching to
// OneClub would do. During the history new members join through the
// membership application flow (approval, fee payment, activation) and
// members renew before their membership ends.

import (
	"context"
	"encoding/csv"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/app/rhapsody"
	"oneclub/internal/kernel/dbtx"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "people", Order: 10, Setup: trialPeopleSetup, Day: trialPeopleDay})
}

const (
	trialMembership      = "membership@demo.oneclub.id"
	trialMembershipAdmin = "membership.admin@demo.oneclub.id"
)

var (
	trialMaleNames = []string{"Agus", "Budi", "Hendra", "Andi", "Rudi", "Dedi", "Eko", "Fajar", "Gunawan", "Hari", "Irwan", "Joko", "Kurniawan",
		"Lukman", "Made", "Rahmat", "Slamet", "Teguh", "Wahyu", "Yudi", "Bambang", "Dimas", "Rizky", "Arief", "Bayu", "Hadi", "Iwan", "Tommy",
		"Stefanus", "Michael", "William", "Kevin", "Hendrik", "Robert", "Steven", "Albert", "Johan", "Felix", "Ronald", "Daniel"}
	trialFemaleNames = []string{"Sari", "Dewi", "Rina", "Wati", "Siti", "Lestari", "Maya", "Indah", "Putri", "Ratna", "Yuli", "Fitri", "Nadia",
		"Citra", "Ayu", "Melati", "Kartika", "Novi", "Diana", "Grace", "Jessica", "Linda", "Monica", "Vivian", "Clara", "Sinta", "Anita", "Rosa",
		"Tania", "Wulan"}
	trialLastNames = []string{"Wijaya", "Santoso", "Gunawan", "Halim", "Tanoto", "Kusuma", "Susanto", "Hartono", "Pratama", "Saputra", "Wibowo",
		"Setiawan", "Hidayat", "Nugroho", "Siregar", "Nasution", "Simanjuntak", "Sitompul", "Lubis", "Harahap", "Tanjung", "Salim", "Sutanto",
		"Budiman", "Hakim", "Tjahjadi", "Lim", "Tan", "Gozali", "Hermawan", "Purnomo", "Rahardjo", "Sugiarto", "Wahyudi", "Kurnia"}
	trialCities     = []string{"Tangerang", "Tangerang Selatan", "Jakarta Barat", "Jakarta Selatan", "Jakarta Pusat", "Bekasi", "Bogor", "Depok"}
	trialCorporates = [][2]string{{"PT Cahaya Nusantara Abadi", "01.111.222.3-411.000"}, {"PT Graha Mitra Sejahtera", "01.222.333.4-412.000"},
		{"PT Bintang Timur Logistik", "01.333.444.5-413.000"}, {"PT Samudra Karya Mandiri", "01.444.555.6-414.000"},
		{"PT Puncak Investama", "01.555.666.7-415.000"}, {"CV Harmoni Teknik", "01.666.777.8-416.000"}}
)

// trialPerson is one generated person.
type trialPerson struct {
	Legacy, Name, Gender, Email, Phone, City, Birth string
	Handicap                                        string
}

func trialPersonOf(r *rand.Rand, n int, gender string) trialPerson {
	first := trialMaleNames[r.IntN(len(trialMaleNames))]
	if gender == "F" {
		first = trialFemaleNames[r.IntN(len(trialFemaleNames))]
	}
	last := trialLastNames[r.IntN(len(trialLastNames))]
	birth := time.Date(1950+r.IntN(50), time.Month(1+r.IntN(12)), 1+r.IntN(28), 0, 0, 0, 0, time.UTC)
	hcp := ""
	if r.IntN(3) > 0 {
		hcp = fmt.Sprintf("%.1f", 6+r.Float64()*26)
	}
	return trialPerson{Legacy: fmt.Sprintf("C%04d", n), Name: first + " " + last, Gender: gender,
		Email: strings.ToLower(first+"."+last) + fmt.Sprintf("%d@mail.trial.test", n),
		Phone: fmt.Sprintf("+62812%08d", 31000000+n*37), City: trialCities[r.IntN(len(trialCities))], Birth: birth.Format(time.DateOnly), Handicap: hcp}
}

// trialPeopleSetup migrates the club's existing people at go-live.
func trialPeopleSetup(ctx context.Context, t *Trial) error {
	r := t.Rand("people")
	golive := t.Start.AddDate(0, 0, -1)
	type member struct {
		no, legacy, typ, starts, ends, status, principal, rel, corp string
	}
	var people []trialPerson
	var members []member
	var nominees [][2]string
	n := 0
	person := func(g string) trialPerson {
		n++
		p := trialPersonOf(r, n, g)
		people = append(people, p)
		return p
	}
	gender := func() string {
		if r.IntN(4) == 0 {
			return "F"
		}
		return "M"
	}
	period := func(active bool) (string, string, string) {
		if !active { // ended before go-live
			s := golive.AddDate(-1, 0, -30-r.IntN(200))
			return s.Format(time.DateOnly), s.AddDate(1, 0, -1).Format(time.DateOnly), "expired"
		}
		s := golive.AddDate(0, 0, -20-r.IntN(340))
		return s.Format(time.DateOnly), s.AddDate(1, 0, -1).Format(time.DateOnly), "active"
	}
	mno := 1000
	nextNo := func() string { mno++; return fmt.Sprintf("M%d", mno) }
	for i := range 70 { // individual members (a few expired before go-live)
		p := person(gender())
		s, e, st := period(i%12 != 11)
		members = append(members, member{no: nextNo(), legacy: p.Legacy, typ: "IND", starts: s, ends: e, status: st})
	}
	for range 18 { // family memberships: principal, spouse, sometimes a child
		p := person("M")
		s, e, st := period(true)
		pno := nextNo()
		members = append(members, member{no: pno, legacy: p.Legacy, typ: "FAM", starts: s, ends: e, status: st})
		sp := person("F")
		members = append(members, member{no: nextNo(), legacy: sp.Legacy, typ: "FAM", starts: s, ends: e, status: st, principal: pno, rel: "spouse"})
		if r.IntN(2) == 0 {
			c := person(gender())
			members = append(members, member{no: nextNo(), legacy: c.Legacy, typ: "FAM", starts: s, ends: e, status: st, principal: pno, rel: "child"})
		}
	}
	for i := range trialCorporates { // corporate memberships: two nominees each
		for range 2 {
			p := person(gender())
			s, e, st := period(true)
			members = append(members, member{no: nextNo(), legacy: p.Legacy, typ: "CORP", starts: s, ends: e, status: st, corp: fmt.Sprintf("K%02d", i+1)})
			nominees = append(nominees, [2]string{fmt.Sprintf("K%02d", i+1), p.Legacy})
		}
	}
	for range 150 { // golf visitors and other customers
		person(gender())
	}

	dir, err := os.MkdirTemp("", "oneclub-trial-rhapsody-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	write := func(name string, header []string, rows [][]string) error {
		f, err := os.Create(filepath.Join(dir, name+".csv")) //nolint:gosec // G304: temporary directory of this run
		if err != nil {
			return err
		}
		w := csv.NewWriter(f)
		_ = w.Write(header)
		_ = w.WriteAll(rows)
		w.Flush()
		if err := w.Error(); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	var rows [][]string
	for _, p := range people {
		rows = append(rows, []string{p.Legacy, p.Name, p.Gender, p.Birth, p.Email, p.Phone, "Jl. Trial No. " + p.Legacy[1:], p.City, p.Handicap})
	}
	if err := write("customers", []string{"legacy_id", "name", "gender", "birth_date", "email", "phone", "address", "city", "handicap_index"}, rows); err != nil {
		return err
	}
	rows = nil
	for i, c := range trialCorporates {
		rows = append(rows, []string{fmt.Sprintf("K%02d", i+1), c[0], c[1], "HR & GA Manager", fmt.Sprintf("hrga%d@corp.trial.test", i+1),
			fmt.Sprintf("+62215%07d", 5550000+i), "Jl. Sudirman Kav. " + fmt.Sprint(10+i) + ", Jakarta"})
	}
	if err := write("corporate_accounts", []string{"legacy_id", "name", "npwp", "contact_name", "email", "phone", "address"}, rows); err != nil {
		return err
	}
	rows = nil
	for _, x := range nominees {
		rows = append(rows, []string{x[0], x[1], "Director"})
	}
	if err := write("corporate_nominees", []string{"corporate_legacy_id", "customer_legacy_id", "title"}, rows); err != nil {
		return err
	}
	rows = nil
	var balances [][]string
	for _, m := range members {
		rows = append(rows, []string{m.no, m.legacy, m.typ, m.starts, m.ends, m.status, "MG-" + m.no, m.principal, m.rel, m.corp})
		if m.principal == "" && m.status == "active" {
			balances = append(balances, []string{m.no, "0", golive.Format(time.DateOnly)})
		}
	}
	if err := write("members", []string{"member_no", "customer_legacy_id", "type_code", "starts_on", "ends_on", "status", "card_number",
		"principal_member_no", "relationship", "corporate_legacy_id"}, rows); err != nil {
		return err
	}
	if err := write("opening_balances", []string{"member_no", "balance", "as_of"}, balances); err != nil {
		return err
	}

	a := t.App
	if _, err := rhapsody.Stage(ctx, a.DB, dir); err != nil {
		return fmt.Errorf("migration stage: %w", err)
	}
	d := &rhapsody.Deps{DB: a.DB, Engine: a.Engine, Golf: a.Golf, Billing: a.Billing, Location: a.Instance.Location,
		P2: rhapsody.P2Deps{Vouchers: a.Vouchers, Reservations: a.Reservations, Experience: a.Experience}}
	issues, err := d.Validate(ctx, t.Property)
	if err != nil {
		return err
	}
	if len(issues) > 0 {
		return fmt.Errorf("migration validation: %d issues, first %+v", len(issues), issues[0])
	}
	reps, err := d.Load(ctx, t.Property)
	if err != nil {
		return fmt.Errorf("migration load: %w", err)
	}
	for _, rep := range reps {
		if rep.Failed > 0 {
			return fmt.Errorf("migration load %s: %d failed: %v", rep.Entity, rep.Failed, rep.Errors)
		}
	}
	return nil
}

// trialMember is a migrated or new member of the trial.
type trialMember struct {
	No, Name, Phone, Type string
	CustomerID            uuid.UUID
	Principal             bool
	StartsOn, EndsOn      time.Time // start of the first / end of the latest active membership
}

// Members returns the active members (cached for the run).
func (t *Trial) Members() []trialMember {
	if t.members != nil {
		return t.members
	}
	ctx := dbtx.System(context.Background())
	t.check(t.App.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT m.code, m.name, coalesce(m.phone, ''), coalesce(m.membership_type, ''), m.customer_id,
				EXISTS (SELECT 1 FROM membership.memberships ms WHERE ms.member_id = m.id AND ms.role = 'principal'),
				coalesce((SELECT min(ms.starts_on) FROM membership.memberships ms WHERE ms.member_id = m.id AND ms.status = 'active'), '2000-01-01'),
				coalesce((SELECT max(coalesce(ms.ends_on, '9999-12-31')) FROM membership.memberships ms WHERE ms.member_id = m.id AND ms.status = 'active'), '2000-01-01')
			FROM membership.members m WHERE m.property_id = $1 AND m.status = 'active' AND m.customer_id IS NOT NULL ORDER BY m.code`, t.Property)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m trialMember
			if err := rows.Scan(&m.No, &m.Name, &m.Phone, &m.Type, &m.CustomerID, &m.Principal, &m.StartsOn, &m.EndsOn); err != nil {
				return err
			}
			t.members = append(t.members, m)
		}
		return rows.Err()
	}))
	return t.members
}

// trialGuest is a non-member customer.
type trialGuest struct {
	ID          uuid.UUID
	Name, Phone string
}

// MembersOn returns the members whose membership covers a day.
func (t *Trial) MembersOn(day time.Time) []trialMember {
	var out []trialMember
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	for _, m := range t.Members() {
		if !m.EndsOn.Before(d) && !m.StartsOn.After(d) {
			out = append(out, m)
		}
	}
	return out
}

// Guests returns the migrated non-member customers (cached).
func (t *Trial) Guests() []trialGuest {
	if t.guests != nil {
		return t.guests
	}
	ctx := dbtx.System(context.Background())
	t.check(t.App.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT c.id, c.name, coalesce(c.phone, '') FROM crm.customers c WHERE c.property_id = $1 AND c.code LIKE 'RH-C%'
			AND NOT EXISTS (SELECT 1 FROM membership.members m WHERE m.customer_id = c.id) ORDER BY c.code`, t.Property)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g trialGuest
			if err := rows.Scan(&g.ID, &g.Name, &g.Phone); err != nil {
				return err
			}
			t.guests = append(t.guests, g)
		}
		return rows.Err()
	}))
	return t.guests
}

// trialPeopleDay: new members join (every few days) and members renew
// before their membership ends.
func trialPeopleDay(ctx context.Context, t *Trial, day time.Time) error {
	r := t.Rand("people:" + day.Format(time.DateOnly))
	t.At(day, "10:00")
	mm := t.As(trialMembership)
	cashier := t.As(trialCashier)
	// renewals: memberships ending in 10–30 days renew on a member-specific day
	type due struct {
		ID, Package uuid.UUID
		Ends        time.Time
		No          string
	}
	var dues []due
	t.check(t.App.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT ms.id, coalesce(ms.package_id, (SELECT p.id FROM membership.packages p WHERE p.type_id = ms.type_id ORDER BY p.code LIMIT 1)),
			ms.ends_on, m.code FROM membership.memberships ms JOIN membership.members m ON m.id = ms.member_id
			WHERE ms.property_id = $1 AND ms.role = 'principal' AND ms.status = 'active' AND ms.ends_on BETWEEN $2::date + 10 AND $2::date + 30
			AND NOT EXISTS (SELECT 1 FROM membership.renewals rn WHERE rn.membership_id = ms.id AND rn.status IN ('pending', 'completed'))
			ORDER BY m.code`, t.Property, day.Format(time.DateOnly))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.ID, &d.Package, &d.Ends, &d.No); err != nil {
				return err
			}
			dues = append(dues, d)
		}
		return rows.Err()
	}))
	for _, d := range dues {
		h := t.Rand("renew:" + d.No)
		if h.IntN(8) == 0 { // some members let the membership lapse
			continue
		}
		left := int(d.Ends.Sub(time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
		if left != 10+h.IntN(20) && left != 10 {
			continue
		}
		res := mm.Post("/api/v1/membership/memberships/"+d.ID.String()+":renew", J{"packageId": d.Package})
		if fid := res.S("folioId"); fid != "" && res.S("status") == "pending" {
			trialPayFolio(t, cashier, fid, []string{"bank_transfer", "card", "cash"}[h.IntN(3)])
		}
	}
	// new member applications (about one every four days)
	if r.IntN(4) == 0 {
		guests := t.Guests()
		g := guests[(day.YearDay()*7)%len(guests)]
		trialJoin(ctx, t, g.ID, []string{"IND", "IND", "FAM"}[r.IntN(3)], r)
	}
	return nil
}

// trialJoin runs a membership application: draft, submit, approval, fee
// payment and activation (P1 flow).
func trialJoin(ctx context.Context, t *Trial, customer uuid.UUID, typeCode string, r *rand.Rand) {
	mm := t.As(trialMembershipAdmin) // the Membership Manager approves
	var typeID, pkgID uuid.UUID
	t.check(t.App.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT t.id, p.id FROM membership.types t JOIN membership.packages p ON p.type_id = t.id
			WHERE t.property_id = $1 AND t.code = $2 ORDER BY p.code LIMIT 1`, t.Property, typeCode).Scan(&typeID, &pkgID)
	}))
	var exists bool
	t.check(t.App.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.applications WHERE customer_id = $1 AND status <> 'cancelled')`, customer).Scan(&exists)
	}))
	if exists {
		return
	}
	body := J{"customerId": customer, "typeId": typeID, "packageId": pkgID, "notes": "Joined after a trial round (trial dataset)"}
	if typeCode == "FAM" {
		n := 5000 + r.IntN(4000)
		p := trialPersonOf(r, n, "F")
		spouse := mm.Post("/api/v1/crm/customers", J{"code": "TRL-SP-" + customer.String()[24:], "name": p.Name, "gender": "female",
			"email": p.Email, "phone": p.Phone, "birthDate": p.Birth}).S("id")
		body["dependents"] = []J{{"customerId": spouse, "relationship": "spouse"}}
	}
	aid := mm.Post("/api/v1/membership/applications", body).S("id")
	app := mm.Post("/api/v1/membership/applications/"+aid+":submit", nil)
	if app.S("status") == "pending" {
		t.ApprovePending(ctx)
		app = mm.Get("/api/v1/membership/applications/" + aid)
	}
	if fid := app.S("feeFolioId"); fid != "" {
		trialPayFolio(t, t.As(trialCashier), fid, "bank_transfer")
		t.check(t.flush(ctx))
		app = mm.Get("/api/v1/membership/applications/" + aid)
	}
	if app.S("status") != "completed" {
		mm.Post("/api/v1/membership/applications/"+aid+":activate", J{})
	}
	t.members = nil // reload
}

// trialPayFolio settles the balance of a folio at the venue.
func trialPayFolio(t *Trial, c *TrialClient, folioID, method string) J {
	f := c.Get("/api/v1/billing/folios/" + folioID)
	bal := f.M("summary").S("balance")
	if bal == "" || strings.HasPrefix(bal, "-") || bal == "0" || bal == "0.00" {
		return f
	}
	body := J{"folioId": folioID, "amount": bal, "methodType": method, "channel": "venue"}
	if method == "card" || method == "qris" {
		body["reference"] = fmt.Sprintf("EDC-%06d", t.Rand(folioID).IntN(1000000))
	}
	return c.Post("/api/v1/billing/payments", body)
}
