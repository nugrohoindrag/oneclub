package inventory

// PRD P5 FR-HR-06: assets in the custody of an employee (custodian set on
// the asset register). HRIS lists them on the offboarding checklist of a
// leaver and keeps the "assets returned" item open while any remains.

import (
	"context"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// AssetsInCustody returns the assets an employee holds (not disposed, not
// archived), by code.
func AssetsInCustody(ctx context.Context, q dbtx.Querier, employee uuid.UUID) ([]AssetSummary, error) {
	rows, err := q.Query(ctx, assetSelect+` WHERE a.custodian_employee_id = $1 AND a.status <> 'disposed' AND a.archived_at IS NULL ORDER BY a.code`, employee)
	return handle.List[AssetSummary](rows, err)
}
