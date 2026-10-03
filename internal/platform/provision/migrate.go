// Package provision runs migrations, synchronises the static catalogue into
// an instance database and provisions new customer instances (EP-01).
package provision

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"oneclub/db/migrations"
)

// RiverSchema holds River's job tables.
const RiverSchema = "river"

// MigrationStatus is the applied version per module.
type MigrationStatus struct {
	Module  string
	Version int64
	Pending int
}

func openSQL(url string) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	return db, nil
}

func providerFor(db *sql.DB, module string) (*goose.Provider, error) {
	sub, err := fs.Sub(migrations.FS, module)
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(goose.DialectPostgres, db, sub,
		goose.WithTableName("goose_"+module),
		goose.WithDisableGlobalRegistry(true))
}

// MigrateUp applies all pending migrations as the database owner, then the
// River job tables. Migrations are expand-only by policy (Technical Doc §7.5).
func MigrateUp(ctx context.Context, ownerURL string) error {
	db, err := openSQL(ownerURL)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, m := range migrations.Order {
		p, err := providerFor(db, m)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", m, err)
		}
		res, err := p.Up(ctx)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", m, err)
		}
		for _, r := range res {
			slog.InfoContext(ctx, "migration applied", "module", m, "version", r.Source.Version, "duration", r.Duration)
		}
	}
	return migrateRiver(ctx, ownerURL, db)
}

func migrateRiver(ctx context.Context, ownerURL string, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+RiverSchema); err != nil {
		return fmt.Errorf("river schema: %w", err)
	}
	pool, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	mig, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Schema: RiverSchema})
	if err != nil {
		return err
	}
	res, err := mig.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("river migrate: %w", err)
	}
	for _, v := range res.Versions {
		slog.InfoContext(ctx, "river migration applied", "version", v.Version)
	}
	_, err = db.ExecContext(ctx, `SELECT platform.grant_app($1)`, RiverSchema)
	return err
}

// Status reports applied and pending migrations per module.
func Status(ctx context.Context, ownerURL string) ([]MigrationStatus, error) {
	db, err := openSQL(ownerURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var out []MigrationStatus
	for _, m := range migrations.Order {
		p, err := providerFor(db, m)
		if err != nil {
			return nil, err
		}
		v, err := p.GetDBVersion(ctx)
		if err != nil {
			v = 0
		}
		st, err := p.Status(ctx)
		if err != nil {
			return nil, err
		}
		pending := 0
		for _, s := range st {
			if s.State == goose.StatePending {
				pending++
			}
		}
		out = append(out, MigrationStatus{Module: m, Version: v, Pending: pending})
	}
	return out, nil
}

// MigrateDown rolls back the last migration of one module (dev only).
func MigrateDown(ctx context.Context, ownerURL, module string) error {
	db, err := openSQL(ownerURL)
	if err != nil {
		return err
	}
	defer db.Close()
	p, err := providerFor(db, module)
	if err != nil {
		return err
	}
	_, err = p.Down(ctx)
	return err
}
