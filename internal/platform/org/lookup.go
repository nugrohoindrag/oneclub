package org

import (
	"context"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
)

// PropertyName returns the name of a property (letters, documents) so
// modules do not read platform tables themselves.
func PropertyName(ctx context.Context, q dbtx.Querier, property uuid.UUID) (string, error) {
	var name string
	err := q.QueryRow(ctx, `SELECT name FROM platform.properties WHERE id = $1`, property).Scan(&name)
	return name, err
}
