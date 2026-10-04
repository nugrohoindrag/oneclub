package sportclub

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
)

// ImportEnrollment loads an active class participant from the legacy system
// (PRD P2 FR-MIG-P2-05): no registration fee is charged again.
func ImportEnrollment(ctx context.Context, tx pgx.Tx, property uuid.UUID, programCode string, customer uuid.UUID, validUntil time.Time) (uuid.UUID, error) {
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM sportclub.class_programs WHERE property_id = $1 AND code = upper($2)`, property, programCode).Scan(&pid); err != nil {
		if dbtx.IsNoRows(err) {
			return uuid.Nil, errs.NotFound("class program " + programCode)
		}
		return uuid.Nil, err
	}
	eid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.enrollments (id, property_id, program_id, customer_id, segment, valid_until, created_by)
		VALUES ($1,$2,$3,$4,'member',$5,$6)`, eid, property, pid, customer, validUntil, actor(ctx)); err != nil {
		return uuid.Nil, err
	}
	return eid, audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: "import", EntityType: "sportclub.enrollment", EntityID: eid.String(),
		EntityLabel: programCode, PropertyID: &property, After: map[string]any{"customerId": customer, "validUntil": validUntil}})
}
