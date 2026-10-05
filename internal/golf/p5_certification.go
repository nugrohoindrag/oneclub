package golf

// Caddy certification enforcement of PRD P5 (FR-TRC-03, contract H7): a
// caddy whose mandatory certification expired (or is missing, depending on
// the HR Configuration) is not offered in the Caddy Queue and cannot be
// assigned. Golf does not import hris (layer 1): internal/app wires the
// check to the HRIS certification register; without it nothing is checked.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
)

// CaddyCertificationCheck returns the caddies (of ids) that may not work on
// a day, with the reason.
type CaddyCertificationCheck func(ctx context.Context, q dbtx.Querier, property uuid.UUID, ids []uuid.UUID, day time.Time) (map[uuid.UUID]string, error)

var caddyCertificationCheck CaddyCertificationCheck

// SetCaddyCertificationCheck wires the certification check (internal/app).
func SetCaddyCertificationCheck(f CaddyCertificationCheck) { caddyCertificationCheck = f }

// uncertifiedCaddies applies the wired check (nil map = all certified).
func uncertifiedCaddies(ctx context.Context, q dbtx.Querier, property uuid.UUID, ids []uuid.UUID, day time.Time) (map[uuid.UUID]string, error) {
	if caddyCertificationCheck == nil || len(ids) == 0 {
		return nil, nil
	}
	return caddyCertificationCheck(ctx, q, property, ids, day)
}
