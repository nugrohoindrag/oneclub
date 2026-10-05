package corehr

// Demo data of Core HR (Modern Golf & Country Club, PRD P5 §16 #8 / #9):
// the organization under the General Manager, grades G1–G7, positions with
// workforce roles, certification types, ~40 employees with contracts
// (PKWTT, PKWT expiring in 25 and 6 days, probation), documents (one
// passport and one medical certificate expiring, warning letters),
// certifications (valid, expiring and expired), training programs and
// sessions, a scheduled resignation, a past resignation and HR letter
// templates. Idempotent: nothing is seeded twice.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/id"
)

type demoUnit struct{ code, name, kind, parent, cc string }

var demoUnits = []demoUnit{
	{"GM-OFFICE", "General Management", "division", "", "GM"},
	{"GOLF-OPS", "Golf Operations", "department", "GM-OFFICE", "GOLF"},
	{"COURSE-MAINT", "Course Maintenance", "department", "GM-OFFICE", "COURSE"},
	{"SPORT", "Sport Club", "department", "GM-OFFICE", "SPORT"},
	{"FNB", "Food & Beverage", "department", "GM-OFFICE", "FNB"},
	{"KITCHEN", "Kitchen", "section", "FNB", "FNB"},
	{"SALES", "Sales & Banquet", "department", "GM-OFFICE", "SALES"},
	{"BUNGALOW", "Bungalow", "department", "GM-OFFICE", "STAY"},
	{"FIN", "Finance & Accounting", "department", "GM-OFFICE", "FIN"},
	{"HR", "Human Resources", "department", "GM-OFFICE", "HR"},
	{"ENG", "Engineering", "department", "GM-OFFICE", "ENG"},
}

type demoGrade struct {
	code, name     string
	level          int
	minSal, maxSal string
}

var demoGrades = []demoGrade{
	{"G1", "Staff", 1, "4900000", "7000000"}, {"G2", "Senior Staff", 2, "6000000", "9000000"}, {"G3", "Supervisor", 3, "8000000", "12000000"},
	{"G4", "Assistant Manager", 4, "11000000", "16000000"}, {"G5", "Manager", 5, "15000000", "24000000"},
	{"G6", "Head of Department", 6, "22000000", "38000000"}, {"G7", "General Manager", 7, "40000000", "75000000"},
}

type demoCertType struct {
	code, name, issuer string
	validity           int
	mandatory          []string
}

var demoCertTypes = []demoCertType{
	{"LIFEGUARD", "Lifeguard Certificate", "Balawista Indonesia", 24, []string{"lifeguard"}},
	{"CPR_BLS", "CPR / Basic Life Support", "Indonesian Red Cross (PMI)", 24, []string{"lifeguard"}},
	{"CADDY", "Caddy Certificate", "Modern Golf Caddy Academy", 24, []string{"caddy"}},
	{"FOOD_HANDLER", "Food Handler Certificate (Penjamah Makanan)", "Dinas Kesehatan Kabupaten Tangerang", 36, []string{"food_handler"}},
	{"K3_ELECTRICAL", "K3 Electrical License", "Kementerian Ketenagakerjaan", 36, []string{"engineering"}},
	{"K3_BOILER", "K3 Boiler Operator License", "Kementerian Ketenagakerjaan", 36, nil},
	{"PESTICIDE", "Pesticide Applicator Certificate", "Kementerian Pertanian", 36, []string{"course_maintenance"}},
	{"FIRST_AID", "First Aid", "Indonesian Red Cross (PMI)", 24, []string{"security", "sport_staff"}},
	{"SPORT_INSTRUCTOR", "Sport Instructor Certification", "Sport federation", 24, []string{"instructor"}},
}

type demoPosition struct {
	code, name, unit, grade string
	head                    bool
	role                    string
	required                []string
	headcount               int
}

var demoPositions = []demoPosition{
	{"GM", "General Manager", "GM-OFFICE", "G7", true, "", nil, 1},
	{"GOLF-MGR", "Golf Manager", "GOLF-OPS", "G6", true, "", nil, 1},
	{"CADDY-MASTER", "Caddy Master", "GOLF-OPS", "G3", false, "", nil, 1},
	{"GOLF-ADMIN", "Golf Administration", "GOLF-OPS", "G2", false, "", nil, 2},
	{"STARTER", "Starter", "GOLF-OPS", "G1", false, "starter", nil, 3},
	{"COURSE-SUPT", "Course Superintendent", "COURSE-MAINT", "G6", true, "", nil, 1},
	{"GREENKEEPER", "Greenkeeper", "COURSE-MAINT", "G1", false, "course_maintenance", nil, 8},
	{"SPORT-MGR", "Sport Club Manager", "SPORT", "G6", true, "", nil, 1},
	{"LIFEGUARD", "Lifeguard", "SPORT", "G1", false, "lifeguard", nil, 4},
	{"SPORT-RECEPT", "Sport Club Receptionist", "SPORT", "G1", false, "", nil, 2},
	{"GYM-INSTR", "Gym Instructor", "SPORT", "G2", false, "instructor", nil, 2},
	{"FNB-MGR", "F&B Manager", "FNB", "G6", true, "", nil, 1},
	{"WAITER", "Waiter", "FNB", "G1", false, "", nil, 8},
	{"BARTENDER", "Bartender", "FNB", "G1", false, "food_handler", nil, 2},
	{"EXEC-CHEF", "Executive Chef", "KITCHEN", "G5", true, "food_handler", nil, 1},
	{"COOK", "Cook", "KITCHEN", "G1", false, "food_handler", nil, 6},
	{"SALES-MGR", "Sales & Marketing Manager", "SALES", "G6", true, "", nil, 1},
	{"BANQUET-MGR", "Banquet Manager", "SALES", "G5", false, "", nil, 1},
	{"SALES-EXEC", "Sales Executive", "SALES", "G2", false, "", nil, 3},
	{"FO-MGR", "Front Office Manager", "BUNGALOW", "G5", true, "", nil, 1},
	{"FO-AGENT", "Front Office Agent", "BUNGALOW", "G1", false, "", nil, 3},
	{"FIN-MGR", "Finance Manager", "FIN", "G6", true, "", nil, 1},
	{"ACCOUNTANT", "Accountant", "FIN", "G3", false, "", nil, 2},
	{"CASHIER", "Cashier", "FIN", "G1", false, "", nil, 3},
	{"HR-MGR", "HR Manager", "HR", "G6", true, "", nil, 1},
	{"HR-ADMIN", "HR Admin", "HR", "G2", false, "", nil, 1},
	{"CHIEF-ENG", "Chief Engineer", "ENG", "G6", true, "engineering", nil, 1},
	{"TECHNICIAN", "Technician", "ENG", "G1", false, "engineering", nil, 4},
	{"BOILER-OP", "Boiler Operator", "ENG", "G1", false, "engineering", []string{"K3_BOILER"}, 1},
	{"SECURITY", "Security Officer", "ENG", "G1", false, "security", nil, 6},
}

// demoEmployee: status probation | contract | permanent | daily | resigned;
// contractEnd is days from today for PKWT.
type demoEmployee struct {
	no, name, gender, position, status string
	joinedDaysAgo, contractEnd         int
	certs                              map[string]int // type code → expiry in days from today
}

var demoEmployees = []demoEmployee{
	{"EMP-00001", "Agus Santoso", "male", "GM", "permanent", 3900, 0, nil},
	{"EMP-00002", "Dewi Kartika", "female", "GOLF-MGR", "permanent", 2900, 0, nil},
	{"EMP-00003", "Bambang Wijaya", "male", "COURSE-SUPT", "permanent", 3100, 0, map[string]int{"PESTICIDE": 400}},
	{"EMP-00004", "Ratna Sari", "female", "SPORT-MGR", "permanent", 2400, 0, map[string]int{"FIRST_AID": 300}},
	{"EMP-00005", "Hendro Prasetyo", "male", "FNB-MGR", "permanent", 2000, 0, nil},
	{"EMP-00006", "Yohanes Lim", "male", "SALES-MGR", "permanent", 1800, 0, nil},
	{"EMP-00007", "Wahyu Hidayat", "male", "FO-MGR", "permanent", 1500, 0, nil},
	{"EMP-00008", "Sari Indah", "female", "FIN-MGR", "permanent", 2700, 0, nil},
	{"EMP-00009", "Nadia Putri", "female", "HR-MGR", "permanent", 2200, 0, nil},
	{"EMP-00010", "Joko Susilo", "male", "CHIEF-ENG", "permanent", 3300, 0, map[string]int{"K3_ELECTRICAL": 500}},
	{"EMP-00011", "Yanti Rahayu", "female", "CADDY-MASTER", "permanent", 2600, 0, nil},
	{"EMP-00012", "Fajar Nugroho", "male", "GOLF-ADMIN", "permanent", 1200, 0, nil},
	{"EMP-00013", "Rizky Ramadhan", "male", "STARTER", "contract", 340, 25, nil},
	{"EMP-00014", "Slamet Riyadi", "male", "GREENKEEPER", "permanent", 1900, 0, map[string]int{"PESTICIDE": 380}},
	{"EMP-00015", "Agung Saputra", "male", "GREENKEEPER", "permanent", 1100, 0, map[string]int{"PESTICIDE": 45}},
	{"EMP-00016", "Bayu Pratama", "male", "LIFEGUARD", "permanent", 900, 0, map[string]int{"LIFEGUARD": 420, "CPR_BLS": 420}},
	{"EMP-00017", "Dimas Anggara", "male", "LIFEGUARD", "contract", 600, 200, map[string]int{"LIFEGUARD": -10, "CPR_BLS": 300}},
	{"EMP-00018", "Citra Lestari", "female", "SPORT-RECEPT", "permanent", 800, 0, nil},
	{"EMP-00019", "Eko Firmansyah", "male", "GYM-INSTR", "permanent", 1000, 0, map[string]int{"SPORT_INSTRUCTOR": 250}},
	{"EMP-00020", "Arief Budiman", "male", "EXEC-CHEF", "permanent", 2100, 0, map[string]int{"FOOD_HANDLER": 700}},
	{"EMP-00021", "Andi Kurniawan", "male", "WAITER", "contract", 150, 210, nil},
	{"EMP-00022", "Siti Aminah", "female", "COOK", "permanent", 1300, 0, map[string]int{"FOOD_HANDLER": 20}},
	{"EMP-00023", "Rudi Hartono", "male", "COOK", "permanent", 1400, 0, map[string]int{"FOOD_HANDLER": 800}},
	{"EMP-00024", "Maria Ulfa", "female", "WAITER", "contract", 420, 310, nil},
	{"EMP-00025", "Bagus Setiawan", "male", "BARTENDER", "permanent", 950, 0, map[string]int{"FOOD_HANDLER": 650}},
	{"EMP-00026", "Linda Wijayanti", "female", "BANQUET-MGR", "permanent", 1700, 0, nil},
	{"EMP-00027", "Kevin Tanoto", "male", "SALES-EXEC", "contract", 358, 6, nil},
	{"EMP-00028", "Melati Sukma", "female", "FO-AGENT", "permanent", 1000, 0, nil},
	{"EMP-00029", "Galih Permana", "male", "ACCOUNTANT", "permanent", 1600, 0, nil},
	{"EMP-00030", "Wati Lestari", "female", "CASHIER", "permanent", 1250, 0, nil},
	{"EMP-00031", "Tari Anggraini", "female", "HR-ADMIN", "permanent", 700, 0, nil},
	{"EMP-00032", "Hadi Gunawan", "male", "TECHNICIAN", "permanent", 1450, 0, map[string]int{"K3_ELECTRICAL": 330}},
	{"EMP-00033", "Tono Sugiarto", "male", "TECHNICIAN", "permanent", 1350, 0, map[string]int{"K3_ELECTRICAL": -35}},
	{"EMP-00034", "Ahmad Fauzi", "male", "SECURITY", "permanent", 1150, 0, map[string]int{"FIRST_AID": 260}},
	{"EMP-00035", "Putri Ayu", "female", "WAITER", "probation", 30, 0, nil},
	{"EMP-00036", "Indra Kusuma", "male", "COOK", "daily", 90, 0, map[string]int{"FOOD_HANDLER": 900}},
	{"EMP-00037", "Nina Marlina", "female", "LIFEGUARD", "probation", 60, 0, map[string]int{"LIFEGUARD": 650, "CPR_BLS": 650}},
	{"EMP-00038", "Dedi Mulyadi", "male", "GREENKEEPER", "daily", 120, 0, nil},
	{"EMP-00039", "Yoga Pratama", "male", "STARTER", "permanent", 980, 0, nil},
	{"EMP-00040", "Sinta Dewi", "female", "SALES-EXEC", "resigned", 820, 0, nil},
}

var demoSalary = map[string]int64{"G1": 5500000, "G2": 7000000, "G3": 9500000, "G4": 13000000, "G5": 18000000, "G6": 28000000, "G7": 55000000}

var demoLetters = []struct{ code, name, kind, title, body string }{
	{"SK-KERJA", "Surat Keterangan Kerja", "employment_certificate", "SURAT KETERANGAN KERJA",
		"Yang bertanda tangan di bawah ini menerangkan bahwa:\n\nNama: {{.Employee.FullName}}\nNo. Karyawan: {{.Employee.EmployeeNo}}\n" +
			"Jabatan: {{.Employee.JobTitle}}\nDepartemen: {{.Employee.OrgUnit}}\n\nadalah karyawan {{.Company.LegalName}} ({{.Property}}) sejak " +
			"{{.Employee.JoinDate}} sampai dengan surat ini diterbitkan.\n\nDemikian surat keterangan ini dibuat untuk dipergunakan sebagaimana mestinya.\n\n" +
			"{{.Company.City}}, {{.Today}}\n\nHR Manager"},
	{"PKWT-AGREEMENT", "Perjanjian Kerja Waktu Tertentu", "employment_agreement", "PERJANJIAN KERJA WAKTU TERTENTU {{.Contract.Number}}",
		"Pada hari ini, {{.Today}}, antara {{.Company.LegalName}} (Perusahaan) dan {{.Employee.FullName}}, NIK {{.Employee.NIK}} (Karyawan), " +
			"disepakati perjanjian kerja {{.Contract.Type}} untuk jabatan {{.Employee.JobTitle}} terhitung sejak {{.Contract.StartDate}} sampai dengan " +
			"{{.Contract.EndDate}}.\n\nUpah pokok per bulan: Rp{{.Contract.BaseSalary}}.\n\nHal-hal yang belum diatur mengikuti Peraturan Perusahaan dan " +
			"peraturan perundang-undangan yang berlaku (UU Cipta Kerja, PP 35/2021).\n\nPerusahaan\t\t\tKaryawan"},
	{"SP1", "Surat Peringatan Pertama", "warning_letter", "SURAT PERINGATAN PERTAMA (SP1)",
		"Kepada {{.Employee.FullName}} ({{.Employee.EmployeeNo}}), {{.Employee.JobTitle}}.\n\nBerdasarkan evaluasi atasan, Perusahaan memberikan Surat " +
			"Peringatan Pertama yang berlaku 6 (enam) bulan sejak {{.Today}}. Pelanggaran berulang dalam masa berlaku dapat berlanjut ke SP2.\n\n" +
			"{{.Company.City}}, {{.Today}}\n\nHR Manager"},
}

// SeedDemo seeds the Core HR demo of a property (idempotent).
func SeedDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var seeded bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.employees WHERE property_id = $1 AND employee_no = 'EMP-00001')`, property).Scan(&seeded); err != nil {
		return err
	}
	if seeded {
		return nil
	}
	day := today(ctx, tx, property)
	at := func(days int) string { return ymd(day.AddDate(0, 0, days)) }

	units := map[string]uuid.UUID{}
	for i, u := range demoUnits {
		var parent *uuid.UUID
		if u.parent != "" {
			p := units[u.parent]
			parent = &p
		}
		uid := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO hris.org_units (id, property_id, parent_id, code, name, unit_type, cost_center, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (property_id, code) DO UPDATE SET parent_id = EXCLUDED.parent_id, name = EXCLUDED.name,
			unit_type = EXCLUDED.unit_type, cost_center = EXCLUDED.cost_center, sort_order = EXCLUDED.sort_order RETURNING id`,
			uid, property, parent, u.code, u.name, u.kind, u.cc, i*10).Scan(&uid); err != nil {
			return fmt.Errorf("org unit %s: %w", u.code, err)
		}
		units[u.code] = uid
	}
	grades := map[string]uuid.UUID{}
	for _, g := range demoGrades {
		gid := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO hris.grades (id, code, name, level, min_salary, max_salary) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, gid, g.code, g.name, g.level, g.minSal, g.maxSal).Scan(&gid); err != nil {
			return err
		}
		grades[g.code] = gid
	}
	certTypes := map[string]uuid.UUID{}
	for _, c := range demoCertTypes {
		cid := id.New()
		mandatory := c.mandatory
		if mandatory == nil {
			mandatory = []string{}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO hris.certification_types (id, code, name, issuer, validity_months, mandatory_for) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, cid, c.code, c.name, c.issuer, c.validity, mandatory).Scan(&cid); err != nil {
			return err
		}
		certTypes[c.code] = cid
	}
	positions := map[string]demoPosition{}
	posIDs := map[string]uuid.UUID{}
	for _, p := range demoPositions {
		pid := id.New()
		var role *string
		if p.role != "" {
			role = &p.role
		}
		req := p.required
		if req == nil {
			req = []string{}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO hris.positions (id, property_id, code, name, org_unit_id, grade_id, is_head, workforce_role,
			required_certifications, headcount) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
			pid, property, p.code, p.name, units[p.unit], grades[p.grade], p.head, role, req, p.headcount).Scan(&pid); err != nil {
			return fmt.Errorf("position %s: %w", p.code, err)
		}
		positions[p.code], posIDs[p.code] = p, pid
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.positions p SET reports_to_position_id = h.id FROM hris.positions h
		WHERE p.property_id = $1 AND h.property_id = $1 AND NOT p.is_head AND h.is_head AND h.org_unit_id = p.org_unit_id`, property); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.positions p SET reports_to_position_id = (SELECT id FROM hris.positions WHERE property_id = $1 AND code = 'GM')
		WHERE p.property_id = $1 AND p.is_head AND p.code <> 'GM'`, property); err != nil {
		return err
	}

	employees := map[string]uuid.UUID{}
	banks := []string{"BCA", "Mandiri", "BNI", "BRI"}
	for i, e := range demoEmployees {
		p := positions[e.position]
		join := day.AddDate(0, 0, -e.joinedDaysAgo)
		status, category, active := e.status, "regular", "active"
		switch e.status {
		case "daily":
			status, category = "contract", "daily"
		case "resigned":
			active = "inactive"
		}
		eid := id.New()
		nik := fmt.Sprintf("36030%02d%02d%02d%05d", 1+i%30, 1+i%12, 80+i%20, 10000+i*37)
		npwp := fmt.Sprintf("%02d%03d%03d%d%03d%03d", 70+i%20, 100+i, 400+i, i%10, 411, 0)
		ptkp := []string{"TK/0", "K/0", "K/1", "K/2", "TK/1", "K/3"}[i%6]
		marital := map[bool]string{true: "married", false: "single"}[ptkp[0] == 'K']
		email := fmt.Sprintf("%s@modern-golf.example", demoSlug(e.name))
		var probEnd *string
		if e.status == "probation" {
			s := ymd(join.AddDate(0, 3, -1))
			probEnd = &s
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.employees (id, property_id, employee_no, full_name, gender, birth_date, birth_place, religion, marital_status,
			nationality, nik, npwp, ptkp_status, bpjs_kesehatan_no, bpjs_ketenagakerjaan_no, email, phone, address, city, postal_code, org_unit_id,
			position_id, grade_id, job_title, employment_status, worker_category, join_date, probation_end_date, permanent_date, status)
			VALUES ($1,$2,$3,$4,$5,$6::date,'Tangerang','Islam',$7,'Indonesia',$8,$9,$10,$11,$12,$13,$14,$15,'Tangerang','15710',$16,$17,$18,$19,$20,$21,
			  $22::date,$23::date,$24::date,$25)`,
			eid, property, e.no, e.name, e.gender, ymd(day.AddDate(-25-i%15, -(i%11), -(i%27))), marital, nik, npwp, ptkp,
			fmt.Sprintf("000%010d", 1234500+i), fmt.Sprintf("2%010d", 9876500+i), email, fmt.Sprintf("+62812%08d", 10000000+i*7919),
			fmt.Sprintf("Jl. Raya Modernland No. %d, Tangerang", 10+i), units[p.unit], posIDs[e.position], grades[p.grade], p.name, status, category,
			ymd(join), probEnd, map[bool]any{true: ymd(join), false: nil}[e.status == "permanent"], active); err != nil {
			return fmt.Errorf("employee %s: %w", e.no, err)
		}
		employees[e.no] = eid
		if p.head {
			if _, err := tx.Exec(ctx, `UPDATE hris.org_units SET head_employee_id = $2 WHERE id = $1`, units[p.unit], eid); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, to_org_unit_id, to_position_id,
			to_grade_id, to_status, reason, status, applied_at) VALUES ($1,$2,$3,'hire',$4::date,$5,$6,$7,$8,'Hired','applied',now())`,
			id.New(), property, eid, ymd(join), units[p.unit], posIDs[e.position], grades[p.grade], status); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.employee_bank_accounts (id, property_id, employee_id, bank_code, bank_name, account_no, account_name)
			VALUES ($1,$2,$3,$4,$4,$5,$6)`, id.New(), property, eid, banks[i%len(banks)], fmt.Sprintf("%010d", 5270000000+int64(i)*104729), e.name); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.employee_emergency_contacts (id, property_id, employee_id, name, relationship, phone, is_primary)
			VALUES ($1,$2,$3,$4,$5,$6,true)`, id.New(), property, eid, "Keluarga "+e.name, map[bool]string{true: "spouse", false: "parent"}[marital == "married"],
			fmt.Sprintf("+62813%08d", 20000000+i*104729%9999999)); err != nil {
			return err
		}
		for _, d := range []struct{ t, title, no string }{{"ktp", "KTP", nik}, {"npwp", "NPWP", npwp}, {"bpjs_kesehatan", "BPJS Kesehatan", ""}} {
			var no *string
			if d.no != "" {
				no = &d.no
			}
			if _, err := tx.Exec(ctx, `INSERT INTO hris.employee_documents (id, property_id, employee_id, document_type, title, document_no, issued_on)
				VALUES ($1,$2,$3,$4,$5,$6,$7::date)`, id.New(), property, eid, d.t, d.title, no, ymd(join)); err != nil {
				return err
			}
		}
		for code, exp := range e.certs {
			issued := day.AddDate(0, 0, exp).AddDate(0, -validityOf(code), 1)
			st := "active"
			if exp < 0 {
				st = "expired"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO hris.certifications (id, property_id, certification_type_id, holder_kind, employee_id, holder_name,
				certificate_no, issuer, issued_on, expires_on, status, expired_notified_at)
				SELECT $1,$2,id,'employee',$3,$4,$5,issuer,$6::date,$7::date,$8, CASE WHEN $8 = 'expired' THEN now() END
				FROM hris.certification_types WHERE code = $9`,
				id.New(), property, eid, e.name, fmt.Sprintf("%s-%s", code, e.no[4:]), ymd(issued), at(exp), st, code); err != nil {
				return err
			}
		}
		if err := seedDemoContract(ctx, tx, property, eid, e, p, grades[p.grade], units[p.unit], posIDs[e.position], join, day); err != nil {
			return fmt.Errorf("contract %s: %w", e.no, err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.sequences (property_id, prefix, year, last_value) VALUES ($1, 'EMP:EMP', 0, $2)
		ON CONFLICT (property_id, prefix, year) DO UPDATE SET last_value = greatest(hris.sequences.last_value, EXCLUDED.last_value)`,
		property, len(demoEmployees)); err != nil {
		return err
	}
	// expiring documents (passport, confidential medical certificate) and
	// warning letters
	for _, d := range []struct {
		emp, t, title string
		expires       int
		level         *int
		confidential  bool
	}{
		{"EMP-00006", "passport", "Passport", 20, nil, false},
		{"EMP-00022", "medical", "Health Certificate (food handler)", 12, nil, true},
		{"EMP-00024", "warning_letter", "SP1 — repeated lateness", 150, intRef(1), true},
		{"EMP-00033", "warning_letter", "SP2 — safety procedure breach", 120, intRef(2), true},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO hris.employee_documents (id, property_id, employee_id, document_type, title, issued_on, expires_on, warning_level,
			confidential) VALUES ($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9)`, id.New(), property, employees[d.emp], d.t, d.title, at(d.expires-180),
			at(d.expires), d.level, d.confidential); err != nil {
			return err
		}
	}
	// a past resignation (turnover) and a scheduled one (offboarding)
	if _, err := tx.Exec(ctx, `UPDATE hris.employees SET employment_status = 'resigned', status = 'inactive', termination_date = $2::date,
		termination_type = 'resigned', termination_reason = 'Continued studies', termination_status = 'completed' WHERE id = $1`,
		employees["EMP-00040"], at(-60)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, from_status, to_status, reason, status,
		applied_at) VALUES ($1,$2,$3,'termination',$4::date,'permanent','resigned','resigned: Continued studies','applied',now())`,
		id.New(), property, employees["EMP-00040"], at(-60)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employees SET termination_date = $2::date, termination_type = 'resigned', termination_reason = 'Relocation',
		termination_status = 'scheduled' WHERE id = $1`, employees["EMP-00028"], at(20)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, from_status, to_status, reason, status)
		VALUES ($1,$2,$3,'termination',$4::date,'permanent','resigned','resigned: Relocation','scheduled')`, id.New(), property, employees["EMP-00028"],
		at(20)); err != nil {
		return err
	}
	for i, it := range NewDemoChecklist() {
		if _, err := tx.Exec(ctx, `INSERT INTO hris.offboarding_items (id, property_id, employee_id, code, label, sort_order, status) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			id.New(), property, employees["EMP-00028"], it[0], it[1], i, map[bool]string{true: "done", false: "pending"}[i == 0]); err != nil {
			return err
		}
	}
	if err := seedDemoTraining(ctx, tx, property, day, employees, certTypes); err != nil {
		return err
	}
	for _, l := range demoLetters {
		if _, err := tx.Exec(ctx, `INSERT INTO hris.letter_templates (id, code, name, letter_type, language, title, body) VALUES ($1,$2,$3,$4,'id',$5,$6)
			ON CONFLICT (code) DO NOTHING`, id.New(), l.code, l.name, l.kind, l.title, l.body); err != nil {
			return err
		}
	}
	return nil
}

// NewDemoChecklist is the default offboarding checklist (code, label).
func NewDemoChecklist() [][2]string {
	return [][2]string{{"handover", "Work handover completed"}, {"return_assets", "Company assets returned (uniform, ID card, keys, devices)"},
		{"revoke_access", "System access revoked"}, {"biometric_removal", "Biometric templates removed from attendance devices"},
		{"final_settlement", "Final settlement calculated (remaining salary, leave, compensation)"}, {"exit_interview", "Exit interview"}}
}

func intRef(n int) *int { return &n }

func validityOf(code string) int {
	for _, c := range demoCertTypes {
		if c.code == code {
			return c.validity
		}
	}
	return 24
}

func demoSlug(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			out = append(out, r+'a'-'A')
		case r >= 'a' && r <= 'z':
			out = append(out, r)
		case r == ' ':
			out = append(out, '.')
		}
	}
	return string(out)
}

func seedDemoContract(ctx context.Context, tx pgx.Tx, property, eid uuid.UUID, e demoEmployee, p demoPosition, grade, unit, pos uuid.UUID,
	join, day time.Time) error {
	if e.status == "daily" {
		return nil
	}
	base := demoSalary[p.grade]
	allowances := []map[string]string{{"code": "TRANSPORT", "name": "Transport Allowance", "amount": "500000"}}
	if p.grade >= "G3" {
		allowances = append(allowances, map[string]string{"code": "POSITION", "name": "Position Allowance", "amount": fmt.Sprint(base / 10)})
	}
	raw, _ := json.Marshal(allowances)
	ctype, probation := "pkwtt", 0
	var end, probEnd *string
	status := "active"
	switch e.status {
	case "contract":
		ctype = "pkwt"
		s := ymd(day.AddDate(0, 0, e.contractEnd))
		end = &s
		if e.contractEnd <= 30 {
			status = "expiring"
		}
	case "probation":
		probation = 3
		s := ymd(join.AddDate(0, 3, -1))
		probEnd = &s
	case "resigned":
		status = "ended"
	}
	no, err := yearlyNumber(ctx, tx, property, "CTR", join.Year())
	if err != nil {
		return err
	}
	var ended *string
	if e.status == "resigned" {
		s := ymd(day.AddDate(0, 0, -61))
		ended = &s
	}
	reminders := []int{}
	if status == "expiring" {
		for _, t := range []int{30, 7} {
			if e.contractEnd <= t {
				reminders = append(reminders, t)
			}
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO hris.contracts (id, property_id, number, employee_id, contract_type, start_date, end_date, probation_months,
		probation_end_date, org_unit_id, position_id, grade_id, job_title, base_salary, allowances, status, activated_at, ended_on, end_reason, reminders_sent)
		VALUES ($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9::date,$10,$11,$12,$13,$14,$15,$16,now(),$17::date,$18,$19)`,
		id.New(), property, no, eid, ctype, ymd(join), end, probation, probEnd, unit, pos, grade, p.name, base, raw, status, ended,
		map[bool]any{true: "resigned", false: nil}[ended != nil], reminders)
	return err
}

func seedDemoTraining(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time, emp map[string]uuid.UUID, certTypes map[string]uuid.UUID) error {
	programs := map[string]uuid.UUID{}
	for _, p := range []struct {
		code, name, cat, cert string
		required              []string
		refresher             *int
		cost                  string
	}{
		{"LG-REFRESH", "Lifeguard Refresher", "safety", "LIFEGUARD", []string{"LIFEGUARD"}, intRef(24), "750000"},
		{"FOOD-HYGIENE", "Food Hygiene & Sanitation", "mandatory", "FOOD_HANDLER", []string{"COOK", "EXEC-CHEF", "BARTENDER"}, intRef(36), "500000"},
		{"SERVICE-EXCELLENCE", "Service Excellence", "service", "", []string{"WAITER", "FO-AGENT", "SPORT-RECEPT"}, nil, "250000"},
		{"FIRE-SAFETY", "Fire Safety & Evacuation", "safety", "", []string{"SECURITY", "TECHNICIAN"}, intRef(12), "0"},
	} {
		var cert *uuid.UUID
		if p.cert != "" {
			c := certTypes[p.cert]
			cert = &c
		}
		pid := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO hris.training_programs (id, property_id, code, name, category, provider, duration_hours, cost_per_participant,
			certification_type_id, required_positions, refresher_months) VALUES ($1,$2,$3,$4,$5,'Modern Golf Academy',8,$6,$7,$8,$9)
			ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
			pid, property, p.code, p.name, p.cat, p.cost, cert, p.required, p.refresher).Scan(&pid); err != nil {
			return err
		}
		programs[p.code] = pid
	}
	loc := time.FixedZone("WIB", 7*3600)
	at := func(days, hour int) time.Time {
		d := day.AddDate(0, 0, days)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, loc)
	}
	for _, s := range []struct {
		program, title, status string
		days                   int
		people                 []string
	}{
		{"SERVICE-EXCELLENCE", "Service Excellence — Batch 3", "completed", -20, []string{"EMP-00021", "EMP-00024", "EMP-00028", "EMP-00018"}},
		{"FOOD-HYGIENE", "Food Hygiene & Sanitation — renewal", "planned", 10, []string{"EMP-00022", "EMP-00036"}},
		{"LG-REFRESH", "Lifeguard Refresher — Q4", "planned", 5, []string{"EMP-00017"}},
	} {
		sid := id.New()
		var done *time.Time
		if s.status == "completed" {
			t := at(s.days, 17)
			done = &t
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.training_sessions (id, property_id, program_id, title, starts_at, ends_at, location, trainer, capacity,
			status, completed_at, cost_total) VALUES ($1,$2,$3,$4,$5,$6,'Clubhouse Training Room','Modern Golf Academy',20,$7,$8,
			CASE WHEN $7 = 'completed' THEN 250000 * $9::int END)`, sid, property, programs[s.program], s.title, at(s.days, 9), at(s.days, 17), s.status, done,
			len(s.people)); err != nil {
			return err
		}
		for i, p := range s.people {
			att, result := "registered", (*string)(nil)
			if s.status == "completed" {
				att = "attended"
				r := "passed"
				if i == len(s.people)-1 {
					att, r = "absent", ""
				}
				if r != "" {
					result = &r
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO hris.training_participants (id, property_id, session_id, employee_id, attendance, result, score)
				VALUES ($1,$2,$3,$4,$5,$6,CASE WHEN $6 = 'passed' THEN 85 END)`, id.New(), property, sid, emp[p], att, result); err != nil {
				return err
			}
		}
	}
	return nil
}
