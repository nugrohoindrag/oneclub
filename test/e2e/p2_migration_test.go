package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"oneclub/internal/migration"
)

// EP-31: `oneclub import rhapsody` — staging, validation, load through the
// module services, idempotent re-run, dry run, reconciliation and sign-off.
func TestP2RhapsodyImport(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	setupGolfCourse(t, sa, "MIG")
	sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "MIG-BALL", "name": "Bola (migrasi)", "kind": "quota",
		"category": "driving_range_balls", "unit": "ball", "faceValue": "5000", "price": "4600000", "validityMonths": 6, "prepaid": true})
	prog := idOf(sa.Must(201, "POST", "/api/v1/membership/programs", map[string]any{"code": "MIG-SPORT", "name": "Sport (migrasi)", "programKind": "sportclub"}))
	sa.Must(201, "POST", "/api/v1/membership/types", map[string]any{"code": "MIG-SC", "name": "Sport Individual", "programId": prog})
	sa.Must(201, "POST", "/api/v1/sportclub/class-programs", map[string]any{"code": "MIG-SWIM", "name": "Swim (migrasi)", "discipline": "swimming", "capacity": 10})
	fac := idOf(sa.Must(201, "POST", "/api/v1/sportclub/facilities", map[string]any{"code": "MIG-SQ", "name": "Squash", "facilityType": "squash",
		"usageMode": "slot_booking", "openingHours": allDay}))
	sa.Must(201, "POST", "/api/v1/sportclub/courts", map[string]any{"code": "MIG-SQ1", "name": "Squash 1", "facilityId": fac})
	court := sa.Must(200, "GET", "/api/v1/sportclub/courts?filter[facilityId]="+fac, nil).Items()[0]
	var resCode string
	sysQueryRow(t, inst, `SELECT code FROM reservation.resources WHERE id = $1`, []any{mustUUID(str(court["resourceId"]))}, &resCode)
	sa.Must(201, "POST", "/api/v1/golf/caddy-levels", map[string]any{"code": "MIG-SR", "name": "Senior (migrasi)", "rank": 2})
	var propCode string
	sysQueryRow(t, inst, `SELECT code FROM platform.properties WHERE id = $1`, []any{inst.Main}, &propCode)

	dir := t.TempDir()
	start := time.Now().AddDate(0, 0, 14).Truncate(time.Hour).UTC()
	files := map[string]string{
		"customers.csv": string(rune(0xFEFF)) + "legacy_id,code,name,phone,email,birth_date\nRC1,MIG-C1,Budi Lama,+6281300000001,budi.lama@mig.test,1980-01-01\n" +
			"RC2,MIG-C2,Sari Lama,+6281300000002,,1990-02-02\nRC3,,No Code,,,\n",
		"outlets.csv":        "legacy_id,code,name,outlet_type\nRO1,MIG-RESTO,Resto Lama,restaurant\n",
		"products.csv":       "legacy_id,code,name,category,product_type,price,member_price\nRP1,MIG-NASI,Nasi Lama,Main,food,50000,45000\n",
		"member_charges.csv": "legacy_id,customer_legacy_id,amount,description\nMC1,RC1,1250000,F&B outstanding Sep\nMC2,RC404,10000,unknown customer\n",
		"vouchers.csv": "legacy_id,code,voucher_type_code,customer_legacy_id,original_quantity,remaining_quantity,price_paid,expires_on\n" +
			"RV1,MIGBALL0001,MIG-BALL,RC2,5000,2000,4600000," + time.Now().AddDate(0, 3, 0).Format("2006-01-02") + "\n",
		"memberships.csv": "legacy_id,customer_legacy_id,type_code,membership_no,start_date,end_date,card_no,legacy_card_no\n" +
			"RM1,RC2,MIG-SC,SC-MIG-0001," + time.Now().AddDate(0, -3, 0).Format("2006-01-02") + ",,,OLD-77\n",
		"reservations.csv": "legacy_id,resource_code,start,end,customer_legacy_id,business_line,notes\nRR1," + resCode + "," + start.Format(time.RFC3339) + "," +
			start.Add(time.Hour).Format(time.RFC3339) + ",RC1,sportclub,Weekly squash\n",
		"enrollments.csv":  "legacy_id,program_code,customer_legacy_id,valid_until\nRE1,MIG-SWIM,RC2," + time.Now().AddDate(0, 6, 0).Format("2006-01-02") + "\n",
		"caddies.csv":      "legacy_id,code,name,level_code,joined_on\nRK1,MIG-C77,Caddy Lama,MIG-SR,2015-05-05\n",
		"hio.csv":          "legacy_id,player_name,customer_legacy_id,section_code,hole_number,achieved_on,witnesses\nRH1,Budi Lama,RC1,MIG-A,3,2019-07-07,Andi;Tono\n",
		"hall_of_fame.csv": "legacy_id,category,title,year,division,player_name\nRF1,club_champion,Club Champion 2019,2019,men,Budi Lama\nRF2,club_history,Club founded,1993,,\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deps := inst.App.MigrationDeps()
	dry, err := migration.Run(t.Context(), deps, migration.Options{Scope: "all", Dir: dir, Property: propCode, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM crm.customers WHERE code LIKE 'MIG-C%'`, nil, &n)
	if n != 0 || len(dry.Entities) != 11 {
		t.Fatalf("dry run must roll back: customers=%d entities=%d", n, len(dry.Entities))
	}
	rep, err := migration.Run(t.Context(), deps, migration.Options{Scope: "all", Dir: dir, Property: propCode})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]migration.EntityResult{}
	for _, e := range rep.Entities {
		got[e.Entity] = e
	}
	if got["customers"].Loaded != 2 || got["customers"].Invalid != 1 || got["member_charges"].Invalid != 1 {
		t.Fatalf("validation: %+v", rep.Entities)
	}
	for _, e := range []string{"outlets", "products", "member_charges", "vouchers", "memberships", "reservations", "enrollments", "caddies", "hio", "hall_of_fame"} {
		if got[e].Loaded == 0 {
			t.Fatalf("%s not loaded: %+v", e, got[e])
		}
	}
	rc := rep.Reconciliation
	if rc["voucher_liability_source"] != "1840000" || rc["voucher_liability_oneclub"] != "1840000" || rc["member_charges_source"] != "1250000" ||
		rc["active_members_source"] != "1" || rc["active_members_oneclub"] != "1" {
		t.Fatalf("reconciliation: %v", rc)
	}
	// Re-running (delta at cutover) loads nothing twice.
	again, err := migration.Run(t.Context(), deps, migration.Options{Scope: "customers", Dir: dir, Property: propCode})
	if err != nil {
		t.Fatal(err)
	}
	if again.Entities[0].Skipped != 2 || again.Entities[0].Loaded != 0 {
		t.Fatalf("idempotent re-run: %+v", again.Entities)
	}
	if err := migration.SignOff(t.Context(), inst.DB, rep.BatchID, "Club GM"); err != nil {
		t.Fatal(err)
	}
	if err := migration.SignOff(t.Context(), inst.DB, dry.BatchID, "Club GM"); err == nil {
		t.Fatal("a dry run cannot be signed off")
	}
	// Migrated data is usable: voucher balance, legacy card, HIO history.
	if pb := sa.Must(200, "GET", "/api/v1/commercial/vouchers/MIGBALL0001/balance", nil).JSON(); pb["remaining"] != "2000" {
		t.Fatalf("migrated voucher: %v", pb)
	}
	if c := sa.Must(200, "GET", "/api/v1/membership/cards:lookup?code=OLD-77", nil).JSON(); c["customerName"] != "Sari Lama" {
		t.Fatalf("legacy card lookup: %v", c)
	}
	if h := sa.Must(200, "GET", "/api/v1/golf/hio?filter[status]=completed", nil).Items(); len(h) == 0 {
		t.Fatal("HIO history")
	}
}
