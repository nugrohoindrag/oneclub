package main

// `oneclub import hris` loads the HR migration files of PRD P5 EP-28
// (FR-MIG-P5-01/04): the organization (grades, org units, positions),
// employees with organization placement, employment contracts, employee
// documents (with the scanned files of --documents-dir) and certifications
// of employees, caddies and instructors, as CSV with the columns of the
// HRIS import screen. Loads run in the order grades → org units → positions
// → employees → contracts → documents → certifications and are repeatable;
// rejected rows are listed and skipped. --leave-balances loads the leave
// balances at cut-over (FR-MIG-P5-02, hris/hrtime) after the employees.
// --payroll-ytd loads the opening year-to-date payroll per employee and tax
// year and --legacy-payroll the legacy payroll of a parallel-run period
// (--period YYYY-MM when the file has no periodCode column) for the
// comparison with the OneClub run (FR-MIG-P5-03/05/06, hris/payroll).
// --reconcile compares the legacy control totals (CSV metric,key,legacy)
// with OneClub at --cutover and writes the reconciliation for the HR
// Manager and Finance Manager sign-off (FR-MIG-P5-05).

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/app"
	"oneclub/internal/app/rhapsody"
	"oneclub/internal/hris"
	"oneclub/internal/hris/corehr"
	"oneclub/internal/hris/hrtime"
	"oneclub/internal/hris/payroll"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
)

func runImportHRIS(args []string) error {
	fs := flag.NewFlagSet("import hris", flag.ExitOnError)
	prop := fs.String("property", "MAIN", "target property code")
	flagFor := func(entity, name string) *string {
		return fs.String(name, "", entity+" CSV ("+strings.Join(corehr.ImportColumns[entity], ",")+")")
	}
	files := map[string]*string{
		corehr.ImportGrades: flagFor(corehr.ImportGrades, "grades"), corehr.ImportOrgUnits: flagFor(corehr.ImportOrgUnits, "org-units"),
		corehr.ImportPositions: flagFor(corehr.ImportPositions, "positions"), corehr.ImportEmployees: flagFor(corehr.ImportEmployees, "employees"),
		corehr.ImportContracts: flagFor(corehr.ImportContracts, "contracts"), corehr.ImportDocuments: flagFor(corehr.ImportDocuments, "documents"),
		corehr.ImportCertifications: flagFor(corehr.ImportCertifications, "certifications"),
	}
	docDir := fs.String("documents-dir", "", "directory of the scanned document files named in the file column of --documents (default: the CSV's directory)")
	leave := fs.String("leave-balances", "", "leave balances CSV ("+strings.Join(hrtime.LeaveImportColumns, ",")+")")
	ytd := fs.String("payroll-ytd", "", "opening year-to-date payroll CSV ("+strings.Join(payroll.ImportColumns[payroll.ImportYTD], ",")+")")
	legacy := fs.String("legacy-payroll", "", "legacy payroll CSV of a parallel-run period ("+strings.Join(payroll.ImportColumns[payroll.ImportLegacy], ",")+")")
	period := fs.String("period", "", "period (YYYY-MM) of --legacy-payroll when the file has no periodCode column")
	reconcile := fs.String("reconcile", "", "legacy control totals CSV (metric,key,legacy) to reconcile with OneClub (FR-MIG-P5-05)")
	cutover := fs.String("cutover", "", "cut-over date of --reconcile (YYYY-MM-DD)")
	legacySystem := fs.String("legacy-system", "Legacy HR", "name of the legacy system of --reconcile")
	out := fs.String("out", ".", "directory for the reconciliation report")
	dry := fs.Bool("dry-run", false, "validate only; nothing is saved")
	_ = fs.Parse(args)
	given := *leave != "" || *reconcile != "" || *ytd != "" || *legacy != ""
	for _, f := range files {
		given = given || *f != ""
	}
	if !given {
		return errors.New("usage: oneclub import hris -property CODE [--grades F] [--org-units F] [--positions F] [--employees F] [--contracts F] " +
			"[--documents F [--documents-dir D]] [--certifications F] [--leave-balances F] [--payroll-ytd F] [--legacy-payroll F --period YYYY-MM] " +
			"[--reconcile F --cutover YYYY-MM-DD [--legacy-system NAME] [--out DIR]] [--dry-run]")
	}
	cfg, db, err := load("import")
	if err != nil {
		return err
	}
	defer db.Close()
	a, err := app.Build(cfg, db, app.Options{})
	if err != nil {
		return err
	}
	ctx := context.Background()
	property, err := rhapsody.PropertyByCode(ctx, db, *prop)
	if err != nil {
		return err
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	failed := 0
	for _, entity := range corehr.ImportEntities {
		path := *files[entity]
		if path == "" {
			continue
		}
		f, err := os.Open(path) //nolint:gosec // operator-supplied migration file
		if err != nil {
			return err
		}
		var src io.Reader = f
		if entity == corehr.ImportDocuments {
			dir := *docDir
			if dir == "" {
				dir = filepath.Dir(path)
			}
			rewritten, err := a.HR.Module.UploadImportFiles(ctx, property, f, dir, *dry)
			if err != nil {
				_ = f.Close()
				return fmt.Errorf("documents: %w", err)
			}
			src = strings.NewReader(rewritten)
		}
		rep, err := a.HR.Module.ImportCSV(ctx, property, entity, src, *dry)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", entity, err)
		}
		fmt.Printf("  %-15s rows %5d  inserted %5d  updated %5d  skipped %5d  failed %5d%s\n", entity, rep.Rows, rep.Inserted, rep.Updated, rep.Skipped,
			rep.Failed, map[bool]string{true: "  (dry run)", false: ""}[*dry])
		for _, i := range rep.Issues {
			fmt.Printf("      row %d %s: %s\n", i.Row, i.Key, i.Message)
		}
		failed += rep.Failed
	}
	if *leave != "" {
		f, err := os.Open(*leave) //nolint:gosec // operator-supplied migration file
		if err != nil {
			return err
		}
		rep, err := a.Time.Module.ImportLeaveBalances(ctx, property, f, *dry)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("leave balances: %w", err)
		}
		fmt.Printf("  %-15s rows %5d  inserted %5d  updated %5d  skipped %5d  failed %5d%s\n", "leave balances", rep.Rows, rep.Inserted, rep.Updated, 0,
			rep.Failed, map[bool]string{true: "  (dry run)", false: ""}[*dry])
		for _, i := range rep.Issues {
			fmt.Printf("      row %d %s: %s\n", i.Row, i.Key, i.Message)
		}
		failed += rep.Failed
	}
	for _, x := range []struct{ kind, path, label string }{{payroll.ImportYTD, *ytd, "payroll YTD"}, {payroll.ImportLegacy, *legacy, "legacy payroll"}} {
		if x.path == "" {
			continue
		}
		f, err := os.Open(x.path) //nolint:gosec // operator-supplied migration file
		if err != nil {
			return err
		}
		rep, err := a.Payroll.Module.ImportCSV(ctx, property, x.kind, *period, f, *dry)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", x.label, err)
		}
		fmt.Printf("  %-15s rows %5d  inserted %5d  updated %5d  skipped %5d  failed %5d%s\n", x.label, rep.Rows, rep.Inserted, rep.Updated, 0,
			rep.Failed, map[bool]string{true: "  (dry run)", false: ""}[*dry])
		for _, i := range rep.Issues {
			fmt.Printf("      row %d %s: %s\n", i.Row, i.Key, i.Message)
		}
		failed += rep.Failed
	}
	if *reconcile != "" {
		if err := reconcileHRIS(ctx, a, db, property, *reconcile, *cutover, *legacySystem, *out, *dry); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d rows rejected", failed)
	}
	return nil
}

// reconcileHRIS records the HR migration reconciliation (FR-MIG-P5-05) and
// writes <out>/<number>.csv; on a dry run it prints the comparison only.
func reconcileHRIS(ctx context.Context, a *app.App, db *dbtx.DB, property uuid.UUID, path, cutover, legacy, out string, dry bool) error {
	f, err := os.Open(path) //nolint:gosec // operator-supplied control totals
	if err != nil {
		return err
	}
	records, err := csv.NewReader(f).ReadAll()
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	var lines []hris.ReconciliationInput
	for i, r := range records {
		if len(r) < 3 || (i == 0 && strings.EqualFold(strings.TrimSpace(r[0]), "metric")) {
			continue
		}
		lines = append(lines, hris.ReconciliationInput{Metric: strings.TrimSpace(r[0]), Key: strings.TrimSpace(r[1]), Legacy: strings.TrimSpace(r[2])})
	}
	var rec corehr.MigrationReconciliation
	errDry := errors.New("dry run")
	err = db.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		rec, err = a.HR.Module.CreateReconciliation(ctx, tx, property, corehr.MigrationReconciliationRequest{CutoverDate: cutover, LegacySystem: legacy,
			Lines: lines})
		if err == nil && dry {
			return errDry
		}
		return err
	})
	if err != nil && !errors.Is(err, errDry) {
		return fmt.Errorf("reconcile: %w", err)
	}
	for _, l := range rec.Lines {
		res, lv := "MATCH", "(not in legacy)"
		if !l.Match {
			res = "MISMATCH"
		}
		if l.Legacy != nil {
			lv = *l.Legacy
		}
		fmt.Printf("  %-14s %-24s legacy %-14s oneclub %-14s %s\n", l.Metric, l.Key, lv, l.OneClub, res)
	}
	if dry {
		fmt.Printf("%d checks, %d mismatches (dry run: nothing saved)\n", rec.Checks, rec.Mismatches)
		return nil
	}
	report := filepath.Join(out, rec.Number+".csv")
	if err := os.WriteFile(report, []byte(corehr.ReconciliationCSV(rec)), 0o600); err != nil {
		return err
	}
	fmt.Printf("%s: %d checks, %d mismatches; report %s — sign off in HRIS → Migration (HR Manager and Finance Manager)\n", rec.Number, rec.Checks,
		rec.Mismatches, report)
	return nil
}
