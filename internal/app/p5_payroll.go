package app

// PRD P5 EP-09–15: payroll engine, PPh 21 & BPJS, service charge distribution, commission & bonus payout, caddy & instructor payout runs, payroll accounting and payslips. internal/app/p5.go calls these functions; the area fills them.
//
// Two areas share this file: the payroll core (owner: hris/payroll — the
// functions payrollCore* below) and the partner payouts / distributions
// (p5_payouts.go, called from here with one line each). Payroll reaches
// accounting through hris.payroll_posted / hris.payroll_paid (accounting
// subscribes, p4_accounting.go); time & attendance, Core HR and the payroll
// input sources through the hris root.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris/payroll"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// p5Payroll holds the services of the area.
type p5Payroll struct {
	// Module is the payroll core (runs, structures, statutory rates, payslips).
	Module  *payroll.Module
	Payouts p5Payouts // EP-11–14 payouts & distributions (p5_payouts.go)
}

func p5PayrollContributions() []catalog.Contribution {
	return append(payrollCoreContributions(), p5PayoutsContributions()...)
}

func p5PayrollDocumentTypes() []provision.DocumentType {
	return append(payrollCoreDocumentTypes(), p5PayoutsDocumentTypes()...)
}

func p5PayrollTemplates() []provision.Template {
	return append(payrollCoreTemplates(), p5PayoutsTemplates()...)
}

// buildP5Payroll wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5Payroll(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	a.buildPayrollCore(reg, cfg, db, files)
	a.buildP5Payouts(reg, cfg, db, files) // EP-11–14 (p5_payouts.go)
}

// subscribeP5Payroll registers the event subscribers.
func (a *App) subscribeP5Payroll() {
	// Finance & Accounting posting status of the payroll runs (HRIS phase B)
	for event, h := range a.Payroll.Module.Subscriptions() {
		a.Bus.Subscribe(event, "hris.payroll:"+event, h)
	}
	a.subscribeP5Payouts() // EP-11–14 (p5_payouts.go)
}

// demoP5Payroll seeds the demo data of the area.
func demoP5Payroll(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	if err := demoPayrollCore(ctx, tx, property); err != nil {
		return err
	}
	return demoP5Payouts(ctx, tx, property) // EP-11–14 (p5_payouts.go)
}

// ── payroll core (EP-09, EP-10, EP-15) ────────────────────────────────────

func payrollCoreContributions() []catalog.Contribution {
	return []catalog.Contribution{(&payroll.Module{}).Contribution()}
}
func payrollCoreDocumentTypes() []provision.DocumentType { return payroll.DocumentTypes() }
func payrollCoreTemplates() []provision.Template         { return payroll.Templates() }

// buildPayrollCore wires the payroll module: routes, the approval document
// types of payroll runs and adjustments.
func (a *App) buildPayrollCore(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	m := &payroll.Module{DB: db, Engine: a.Engine, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification,
		StaffURL: func() string { return cfg.PublicBaseURL }, Logo: brandingLogo(files)}
	m.Register(reg)
	a.Approvals.RegisterDocumentType(payroll.RunDocumentType, m.RunDecision)
	a.Approvals.RegisterDocumentType(payroll.AdjustmentDocumentType, m.AdjustmentDecision)
	a.Approvals.RegisterDocumentType(payroll.LoanDocumentType, m.LoanDecision)
	a.Payroll.Module = m
}

// demoPayrollCore seeds pay components and salary structures of the demo
// grades, one posted and paid run of last month and one calculated run of
// this month.
func demoPayrollCore(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	return payroll.SeedDemo(ctx, tx, property)
}
