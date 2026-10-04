package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"

	"oneclub/internal/app"
	"oneclub/internal/migration"
)

// runImport is `oneclub import rhapsody` (Technical Doc §8.2, PRD P2 EP-31):
//
//	oneclub import rhapsody --scope=all|customers|pos|member_charges|vouchers|memberships|reservations|golf_history
//	                        --dir=./extract --property=MAIN [--dry-run]
//	oneclub import sign-off --batch=<id> --by="Name, Title"
func runImport(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: oneclub import rhapsody|sign-off [flags]")
	}
	cfg, db, err := load("import")
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	switch args[0] {
	case "rhapsody":
		fs := flag.NewFlagSet("import rhapsody", flag.ContinueOnError)
		scope := fs.String("scope", "all", "scope to load")
		dir := fs.String("dir", ".", "folder with the CSV extracts")
		prop := fs.String("property", "", "property code")
		dry := fs.Bool("dry-run", false, "validate and roll back")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *prop == "" {
			return errors.New("--property is required")
		}
		a, err := app.Build(cfg, db, app.Options{})
		if err != nil {
			return err
		}
		rep, err := migration.Run(ctx, a.MigrationDeps(), migration.Options{Scope: *scope, Dir: *dir, Property: *prop, DryRun: *dry})
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case "sign-off":
		fs := flag.NewFlagSet("import sign-off", flag.ContinueOnError)
		batch := fs.String("batch", "", "batch id")
		by := fs.String("by", "", "club signatory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		bid, err := uuid.Parse(*batch)
		if err != nil {
			return fmt.Errorf("--batch: %w", err)
		}
		if err := migration.SignOff(ctx, db, bid, *by); err != nil {
			return err
		}
		fmt.Println("signed off", bid)
		return nil
	}
	return errors.New("unknown import command " + args[0])
}
