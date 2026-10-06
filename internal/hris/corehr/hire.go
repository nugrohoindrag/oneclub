package corehr

// Programmatic contract creation for the hire of a recruitment application
// (PRD P5 EP-03 FR-RCT-04): internal/app's hris.Onboarding adapter creates
// the employee through the employee resource and the contract here, with
// the same validation, numbering and audit as POST /api/v1/hris/contracts.

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// CreateContract creates (and with req.Activate activates) an employment
// contract inside the caller's transaction; the request context carries
// the property.
func (m *Module) CreateContract(ctx context.Context, tx pgx.Tx, req ContractRequest) (ContractView, error) {
	return m.createContractHTTP(ctx, tx, nil, req)
}
