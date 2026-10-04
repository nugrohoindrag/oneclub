package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oneclub/internal/app/rhapsody"
)

// EP-31: wave-2 Rhapsody migration on P1's pipeline (rhapsody.Stage →
// Validate → Load → Reconcile): POS master, member F&B charges, vouchers,
// future reservations, class enrollments, caddy profiles, HIO and Hall of
// Fame; a re-run loads nothing twice.
func TestP2RhapsodyImport(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	setupGolfCourse(t, sa, "MIG")
	sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "MIG-BALL", "name": "Bola (migrasi)", "kind": "quota",
		"category": "driving_range_balls", "unit": "ball", "faceValue": "5000", "price": "4600000", "validityMonths": 6, "prepaid": true})
	sa.Must(201, "POST", "/api/v1/sportclub/class-programs", map[string]any{"code": "MIG-SWIM", "name": "Swim (migrasi)", "discipline": "swimming", "capacity": 10})
	fac := idOf(sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "MIG-SQ", "name": "Squash", "facilityType": "squash",
		"usageMode": "slot_booking", "openingHours": allDay}))
	sa.Must(201, "POST", "/api/v1/sportclub/courts", map[string]any{"code": "MIG-SQ1", "name": "Squash 1", "facilityId": fac})
	court := sa.Must(200, "GET", "/api/v1/sportclub/courts?filter[facilityId]="+fac, nil).Items()[0]
	var resCode string
	sysQueryRow(t, inst, `SELECT code FROM reservation.resources WHERE id = $1`, []any{mustUUID(str(court["resourceId"]))}, &resCode)
	sa.Must(201, "POST", "/api/v1/golf/caddy-levels", map[string]any{"code": "MIG-SR", "name": "Senior (migrasi)", "rank": 2})

	dir := t.TempDir()
	start := time.Now().AddDate(0, 0, 14).Truncate(time.Hour).UTC()
	files := map[string]string{
		"customers.csv": string(rune(0xFEFF)) + "legacy_id,name,phone,email,birth_date\nMIG1,Budi Lama,+6281300000001,budi.lama@mig.test,1980-01-01\n" +
			"MIG2,Sari Lama,+6281300000002,,1990-02-02\n",
		"members.csv":        "member_no,customer_legacy_id,type_code,starts_on,status\nMIGM1,MIG1,IND," + time.Now().AddDate(0, -3, 0).Format("2006-01-02") + ",A\n",
		"caddies.csv":        "code,name\nMIG-C77,Caddy Lama\n",
		"outlets.csv":        "code,name,outlet_type\nMIG-RESTO,Resto Lama,restaurant\n",
		"products.csv":       "code,name,category,product_type,price,member_price\nMIG-NASI,Nasi Lama,Main,food,50000,45000\n",
		"member_charges.csv": "legacy_id,member_no,amount,description\nMC1,MIGM1,1250000,F&B outstanding Sep\nMC2,MIGM404,10000,unknown member\n",
		"vouchers.csv": "code,voucher_type_code,customer_legacy_id,original_quantity,remaining_quantity,price_paid,expires_on\n" +
			"MIGBALL0001,MIG-BALL,MIG2,5000,2000,4600000," + time.Now().AddDate(0, 3, 0).Format("2006-01-02") + "\n",
		"reservations.csv": "legacy_id,resource_code,start_at,end_at,customer_legacy_id,business_line,notes\nRR1," + resCode + "," + start.Format(time.RFC3339) + "," +
			start.Add(time.Hour).Format(time.RFC3339) + ",MIG1,sportclub,Weekly squash\n",
		"enrollments.csv":    "legacy_id,program_code,customer_legacy_id,valid_until\nRE1,MIG-SWIM,MIG2," + time.Now().AddDate(0, 6, 0).Format("2006-01-02") + "\n",
		"caddy_profiles.csv": "caddy_code,level_code,joined_on\nMIG-C77,MIG-SR,2015-05-05\n",
		"hio.csv":            "legacy_id,player_name,customer_legacy_id,section_code,hole_number,achieved_on,witnesses\nRH1,Budi Lama,MIG1,MIG-A,3,2019-07-07,Andi;Tono\n",
		"hall_of_fame.csv": "legacy_id,category,title,year,division,player_name\nRF1,club_champion,Club Champion 2019,2019,men,Budi Lama\n" +
			"RF2,club_history,Club founded,1993,,\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	counts, err := rhapsody.Stage(ctx, inst.DB, dir)
	if err != nil {
		t.Fatal(err)
	}
	if counts["vouchers"] != 1 || counts["member_charges"] != 2 || counts["hall_of_fame"] != 2 {
		t.Fatalf("staged: %v", counts)
	}
	a := inst.App
	d := &rhapsody.Deps{DB: inst.DB, Engine: a.Engine, Golf: a.Golf, Billing: a.Billing, Location: a.Instance.Location,
		P2: rhapsody.P2Deps{Vouchers: a.Vouchers, Reservations: a.Reservations, Experience: a.Experience}}
	if _, err := d.Validate(ctx, inst.Main); err != nil {
		t.Fatal(err)
	}
	reps, err := d.Load(ctx, inst.Main)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]rhapsody.LoadReport{}
	for _, r := range reps {
		got[r.Entity] = r
	}
	for _, e := range []string{"outlets", "products", "member_charges", "vouchers", "reservations", "enrollments", "caddy_profiles", "hio", "hall_of_fame"} {
		if got[e].Inserted == 0 {
			t.Fatalf("%s not loaded: %+v", e, got[e])
		}
	}
	if mc := got["member_charges"]; mc.Inserted != 1 || mc.Failed != 1 {
		t.Fatalf("member charge of an unknown member must fail alone: %+v", mc)
	}
	// Re-running (delta at cutover) loads nothing twice.
	again, err := d.Load(ctx, inst.Main)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Inserted > 0 && r.Entity != "member_charges" {
			t.Fatalf("idempotent re-run inserted %s: %+v", r.Entity, r)
		}
	}
	checks, err := d.Reconcile(ctx, inst.Main, map[string]string{"outlets": "1", "products": "1", "voucher_liability": "1840000",
		"enrollments": "1", "hio": "1", "hall_of_fame": "2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		if c.Rhapsody != "(not provided)" && !c.Match {
			t.Fatalf("reconciliation %s: rhapsody=%s oneclub=%s", c.Metric, c.Rhapsody, c.OneClub)
		}
	}
	// Migrated data is usable: voucher balance and HIO history.
	if pb := sa.Must(200, "GET", "/api/v1/commercial/vouchers/MIGBALL0001/balance", nil).JSON(); pb["remaining"] != "2000" {
		t.Fatalf("migrated voucher: %v", pb)
	}
	if h := sa.Must(200, "GET", "/api/v1/golf/hole-in-ones", nil).Items(); len(h) == 0 {
		t.Fatal("HIO history")
	}
}
