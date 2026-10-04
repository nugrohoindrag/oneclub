// Package numbering issues human-readable document numbers per property and
// day, e.g. FOL-20261004-0007 (folios), RSV-… (reservations), ORD-… (POS
// orders). The counter row is locked by the upsert, so concurrent requests
// never receive the same number.
package numbering

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Next returns the next number for prefix at the property on the local day
// of at (callers pass time in the instance timezone).
func Next(ctx context.Context, tx pgx.Tx, property uuid.UUID, prefix string, at time.Time) (string, error) {
	period := at.Format("20060102")
	var n int64
	err := tx.QueryRow(ctx, `INSERT INTO platform.document_sequences (property_id, prefix, period, last_value) VALUES ($1, $2, $3, 1)
		ON CONFLICT (property_id, prefix, period) DO UPDATE SET last_value = platform.document_sequences.last_value + 1
		RETURNING last_value`, property, prefix, period).Scan(&n)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%04d", prefix, period, n), nil
}
