package main

// `oneclub import hris` loads the HR migration files of PRD P5 EP-28
// (FR-MIG-P5-01/04): employees with organization placement, employment
// contracts and certifications of employees, caddies and instructors, as
// CSV with the columns of the HRIS import screen. Loads run in the order
// employees → contracts → certifications and are repeatable; rejected rows
// are listed and skipped. --leave-balances loads the leave balances at
// cut-over (FR-MIG-P5-02, hris/hrtime) after the employees. --payroll-ytd
// loads the opening year-to-date payroll per employee and tax year and
// --legacy-payroll the legacy payroll of a parallel-run period (--period
// YYYY-MM when the file has no periodCode column) for the comparison with
// the OneClub run (FR-MIG-P5-03/05/06, hris/payroll).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"oneclub/internal/app"
	"oneclub/internal/app/rhapsody"
	"oneclub/internal/hris/corehr"
	"oneclub/internal/hris/hrtime"
	"oneclub/internal/hris/payroll"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
)

func runImportHRIS(args []string) error {
	fs := flag.NewFlagSet("import hris", flag.ExitOnError)
	prop := fs.String("property", "MAIN", "target property code")
	employees := fs.String("employees", "", "employees CSV ("+strings.Join(corehr.ImportColumns[corehr.ImportEmployees], ",")+")")
	contracts := fs.String("contracts", "", "contracts CSV ("+strings.Join(corehr.ImportColumns[corehr.ImportContracts], ",")+")")
	certs := fs.String("certifications", "", "certifications CSV ("+strings.Join(corehr.ImportColumns[corehr.ImportCertifications], ",")+")")
	leave := fs.String("leave-balances", "", "leave balances CSV ("+strings.Join(hrtime.LeaveImportColumns, ",")+")")
	ytd := fs.String("payroll-ytd", "", "opening year-to-date payroll CSV ("+strings.Join(payroll.ImportColumns[payroll.ImportYTD], ",")+")")
	legacy := fs.String("legacy-payroll", "", "legacy payroll CSV of a parallel-run period ("+strings.Join(payroll.ImportColumns[payroll.ImportLegacy], ",")+")")
	period := fs.String("period", "", "period (YYYY-MM) of --legacy-payroll when the file has no periodCode column")
	dry := fs.Bool("dry-run", false, "validate only; nothing is saved")
	_ = fs.Parse(args)
	files := map[string]string{corehr.ImportEmployees: *employees, corehr.ImportContracts: *contracts, corehr.ImportCertifications: *certs}
	if *employees == "" && *contracts == "" && *certs == "" && *leave == "" && *ytd == "" && *legacy == "" {
		return errors.New("usage: oneclub import hris -property CODE [--employees FILE] [--contracts FILE] [--certifications FILE] [--leave-balances FILE] " +
			"[--payroll-ytd FILE] [--legacy-payroll FILE --period YYYY-MM] [--dry-run]")
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
		path := files[entity]
		if path == "" {
			continue
		}
		f, err := os.Open(path) //nolint:gosec // operator-supplied migration file
		if err != nil {
			return err
		}
		rep, err := a.HR.Module.ImportCSV(ctx, property, entity, f, *dry)
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
	if failed > 0 {
		return fmt.Errorf("%d rows rejected", failed)
	}
	return nil
}
