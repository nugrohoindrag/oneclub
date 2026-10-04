// Package docno issues human-readable document numbers (booking codes,
// folio and payment numbers …) per property, prefix and day from a module's
// own sequences table, inside the business transaction so numbers are
// gap-free per committed document.
package docno

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Daily returns PREFIX-YYMMDD-NNNN using table (schema.sequences with columns
// property_id, prefix, day, last_value). day is the business date.
func Daily(ctx context.Context, tx pgx.Tx, table string, property uuid.UUID, prefix string, day time.Time) (string, error) {
	var n int
	d := day.Format("2006-01-02")
	err := tx.QueryRow(ctx, `INSERT INTO `+table+` (property_id, prefix, day, last_value) VALUES ($1, $2, $3::date, 1)
		ON CONFLICT (property_id, prefix, day) DO UPDATE SET last_value = `+table+`.last_value + 1
		RETURNING last_value`, property, prefix, d).Scan(&n)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%04d", prefix, day.Format("060102"), n), nil
}

// Running returns PREFIX-NNNNNN from a table without a day column
// (schema.sequences with property_id, prefix, last_value).
func Running(ctx context.Context, tx pgx.Tx, table string, property uuid.UUID, prefix string) (string, int, error) {
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO `+table+` (property_id, prefix, last_value) VALUES ($1, $2, 1)
		ON CONFLICT (property_id, prefix) DO UPDATE SET last_value = `+table+`.last_value + 1
		RETURNING last_value`, property, prefix).Scan(&n)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%s-%06d", prefix, n), n, nil
}
