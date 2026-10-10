package app

// Navigation context (docs/requirement-booking-sportclub-mgcc.md): the
// Member App menu follows the member's active programs (golf, sport_club;
// FR-108) and the Ops areas the HRIS work areas of the employee (FR-99).

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/hris"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/membership"
	"oneclub/internal/platform/navigation"
)

func (a *App) navContext(ctx context.Context, shell string) (navigation.NavContext, error) {
	out := navigation.NavContext{}
	p := authz.From(ctx)
	if p == nil || a.DB == nil {
		return out, nil
	}
	err := a.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		switch shell {
		case "member":
			c, err := crm.CustomerByUser(ctx, tx, p.UserID)
			if err != nil {
				return nil //nolint:nilerr // not a customer yet: the guest menu
			}
			act, err := membership.ActiveFor(ctx, tx, c.PropertyID, c.ID)
			if err != nil {
				return err
			}
			for _, m := range act {
				if (m.ProgramKind == navigation.ProgramGolf || m.ProgramKind == navigation.ProgramSport) && !slices.Contains(out.Programs, m.ProgramKind) {
					out.Programs = append(out.Programs, m.ProgramKind)
				}
			}
		case "ops":
			e, err := hris.EmployeeByUser(ctx, tx, p.UserID)
			if err != nil || e == nil {
				return nil //nolint:nilerr // no employee profile: the role decides
			}
			return tx.QueryRow(ctx, `SELECT work_areas FROM hris.employees WHERE id = $1`, e.ID).Scan(&out.WorkAreas)
		}
		return nil
	})
	return out, err
}
