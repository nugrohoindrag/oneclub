package golf

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
)

// HoleByLabel resolves a hole from section code + hole number (migration).
func HoleByLabel(ctx context.Context, q dbtx.Querier, property uuid.UUID, section string, number int) (uuid.UUID, error) {
	var hid uuid.UUID
	err := q.QueryRow(ctx, `SELECT h.id FROM golf.holes h JOIN golf.course_sections s ON s.id = h.section_id WHERE s.property_id = $1 AND s.code = upper($2)
		AND h.number = $3`, property, section, number).Scan(&hid)
	if dbtx.IsNoRows(err) {
		return hid, errs.NotFound("hole " + section + "-" + itoa(number))
	}
	return hid, err
}

// ImportHIO loads a historical Hole-in-One (PRD P2 FR-MIG-P2-06) as
// completed: it was verified and settled in the legacy process.
func (m *Module) ImportHIO(ctx context.Context, tx pgx.Tx, property uuid.UUID, in HIOInput) (HIO, error) {
	r, err := m.CreateHIO(ctx, tx, property, in)
	if err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.hio_records SET status = 'completed', verified_at = achieved_on::timestamptz,
		notes = coalesce(notes || ' ', '') || '(migrated)' WHERE id = $1`, r.ID); err != nil {
		return r, err
	}
	return m.hio(ctx, tx, r.ID)
}
