// Package dbtx owns database pools and transactions. Every transaction sets
// the Row Level Security context (`SET LOCAL app.property_ids`, FR-TEC-04,
// Technical Doc §7.1) from the RLS scope carried in the context, so services
// never open transactions manually (Technical Doc §5.3).
package dbtx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Scope is the RLS scope of a unit of work.
type Scope struct {
	AllProperties bool        // instance-wide principal or system job
	PropertyIDs   []uuid.UUID // allowed properties when not AllProperties
	UserID        uuid.UUID
}

type scopeKey struct{}

// WithScope attaches an RLS scope to ctx.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFrom returns the RLS scope; the zero scope sees no property rows.
func ScopeFrom(ctx context.Context) Scope {
	s, _ := ctx.Value(scopeKey{}).(Scope)
	return s
}

// System returns ctx with an all-properties scope for jobs and provisioning.
func System(ctx context.Context) context.Context {
	return WithScope(ctx, Scope{AllProperties: true})
}

// DB bundles the primary pool and the reporting read replica pool.
type DB struct {
	Primary *pgxpool.Pool
	Replica *pgxpool.Pool // equals Primary when no replica is configured
}

// Open connects to primary and (optionally distinct) replica.
func Open(ctx context.Context, primaryURL, replicaURL string, maxConns int32) (*DB, error) {
	p, err := newPool(ctx, primaryURL, maxConns)
	if err != nil {
		return nil, fmt.Errorf("dbtx: primary: %w", err)
	}
	db := &DB{Primary: p, Replica: p}
	if replicaURL != "" && replicaURL != primaryURL {
		r, err := newPool(ctx, replicaURL, maxConns/2+1)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("dbtx: replica: %w", err)
		}
		db.Replica = r
	}
	return db, nil
}

func newPool(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	// PgBouncer in transaction mode cannot use server-side prepared statements.
	if strings.Contains(url, "pgbouncer=true") {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func (db *DB) Close() {
	if db.Replica != nil && db.Replica != db.Primary {
		db.Replica.Close()
	}
	db.Primary.Close()
}

// WithTx runs fn in a read-write transaction on the primary with RLS scope.
func (db *DB) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return run(ctx, db.Primary, pgx.TxOptions{}, fn)
}

// WithReadTx runs fn in a read-only transaction on the primary.
func (db *DB) WithReadTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return run(ctx, db.Primary, pgx.TxOptions{AccessMode: pgx.ReadOnly}, fn)
}

// WithReportTx runs fn read-only on the reporting replica (FR-REP-01).
func (db *DB) WithReportTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return run(ctx, db.Replica, pgx.TxOptions{AccessMode: pgx.ReadOnly}, fn)
}

func run(ctx context.Context, pool *pgxpool.Pool, opts pgx.TxOptions, fn func(tx pgx.Tx) error) (err error) {
	tx, err := pool.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = ApplyScope(ctx, tx); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ApplyScope sets the transaction-local RLS settings.
func ApplyScope(ctx context.Context, tx pgx.Tx) error {
	s := ScopeFrom(ctx)
	ids := make([]string, len(s.PropertyIDs))
	for i, id := range s.PropertyIDs {
		ids[i] = id.String()
	}
	all := "off"
	if s.AllProperties {
		all = "on"
	}
	uid := ""
	if s.UserID != uuid.Nil {
		uid = s.UserID.String()
	}
	_, err := tx.Exec(ctx,
		`SELECT set_config('app.all_properties', $1, true),
		        set_config('app.property_ids', $2, true),
		        set_config('app.user_id', $3, true)`,
		all, "{"+strings.Join(ids, ",")+"}", uid)
	return err
}

// Querier is satisfied by pgx.Tx and pools.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PG error helpers.

func pgCode(err error) (string, string) {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code, pe.ConstraintName
	}
	return "", ""
}

// IsUniqueViolation reports a unique constraint violation; returns constraint.
func IsUniqueViolation(err error) (bool, string) {
	c, n := pgCode(err)
	return c == "23505", n
}

// IsForeignKeyViolation reports a foreign key violation.
func IsForeignKeyViolation(err error) bool { c, _ := pgCode(err); return c == "23503" }

// IsExclusionViolation reports an EXCLUDE constraint violation.
func IsExclusionViolation(err error) bool { c, _ := pgCode(err); return c == "23P01" }

// IsCheckViolation reports a CHECK constraint violation.
func IsCheckViolation(err error) (bool, string) {
	c, n := pgCode(err)
	return c == "23514", n
}

// IsInsufficientPrivilege reports SQLSTATE 42501 (e.g. RLS WITH CHECK).
func IsInsufficientPrivilege(err error) bool { c, _ := pgCode(err); return c == "42501" }

// IsNoRows reports pgx.ErrNoRows.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
