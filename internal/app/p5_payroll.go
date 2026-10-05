package app

// PRD P5 EP-09–15: payroll engine, PPh 21 & BPJS, service charge distribution, commission & bonus payout, caddy & instructor payout runs, payroll accounting and payslips. internal/app/p5.go calls these functions; the area fills them.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// p5Payroll holds the services of the area.
type p5Payroll struct {
	Payouts p5Payouts // EP-11–14 payouts & distributions (p5_payouts.go)
}

func p5PayrollContributions() []catalog.Contribution   { return p5PayoutsContributions() }
func p5PayrollDocumentTypes() []provision.DocumentType { return p5PayoutsDocumentTypes() }
func p5PayrollTemplates() []provision.Template         { return p5PayoutsTemplates() }

// buildP5Payroll wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5Payroll(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	a.buildP5Payouts(reg, cfg, db, files) // EP-11–14 (p5_payouts.go)
}

// subscribeP5Payroll registers the event subscribers.
func (a *App) subscribeP5Payroll() {
	a.subscribeP5Payouts() // EP-11–14 (p5_payouts.go)
}

// demoP5Payroll seeds the demo data of the area.
func demoP5Payroll(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	return demoP5Payouts(ctx, tx, property) // EP-11–14 (p5_payouts.go)
}
