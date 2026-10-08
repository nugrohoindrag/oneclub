package app

// PRD P4 EP-16–23 Accounting: general ledger, automatic posting, AR, AP,
// cash & bank, revenue & tax, financial reports, transition & opening
// balances (owner: accounting). Accounting sits in the back office layer:
// it consumes the events of billing, commercial, golf, sportclub,
// membership, crm, inventory and procurement by name (Tech Doc §4.3) and
// gives billing the financial period status (contract K10).

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/accounting"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
	"oneclub/internal/reporting"
)

// p4Accounting holds the services of the area.
type p4Accounting struct {
	Module *accounting.Module
}

// p4AccountingContributions are the catalogue contributions (permissions, role mappings).
func p4AccountingContributions() []catalog.Contribution {
	return []catalog.Contribution{accounting.Contribution()}
}

// p4AccountingDocumentTypes are the approval document types.
func p4AccountingDocumentTypes() []provision.DocumentType { return accounting.DocumentTypes() }

// p4AccountingTemplates are the notification templates.
func p4AccountingTemplates() []provision.Template { return accounting.Templates() }

// buildP4Accounting wires routes, hooks, approval decisions and jobs.
func (a *App) buildP4Accounting(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	m := &accounting.Module{DB: db, Events: a.Bus, Approvals: a.Approvals, Files: files, Engine: a.Engine, Integrations: a.Integrations}
	m.Register(reg, a.Engine)
	m.RegisterP4FixFinance(reg) // P4 gap fixes: drill-down, e-Faktur on invoices, Excel TB comparison
	m.RegisterDashboard(reg)    // Finance Dashboard of the Back Office (budget from the KPI target plan)
	m.RegisterFollowUps(reg)    // Collections (AR) and Vendor Follow-up (AP)
	m.Targets = reporting.MonthTargets
	a.Approvals.RegisterDocumentType(accounting.ManualJournalDocumentType, m.ManualJournalDecision)
	a.Approvals.RegisterDocumentType(accounting.ReopenDocumentType, m.ReopenDecision)
	a.Approvals.RegisterDocumentType(accounting.PaymentRunDocumentType, m.PaymentRunDecision)
	a.Approvals.RegisterDocumentType(accounting.AllowanceDocumentType, m.AllowanceDecision)
	m.RegisterJobs(a.Registrar, a.Instance.Location)
	// K10: billing refuses corrections dated in a soft-closed or closed
	// financial period.
	a.Billing.RegisterPeriodGuard(func(ctx context.Context, q dbtx.Querier, property uuid.UUID, date time.Time) (bool, error) {
		st, err := accounting.PeriodStatus(ctx, q, property, date)
		return st != accounting.PeriodOpen, err
	})
	// FR-TRS-04: the Accounting Export stops at the transition sign-off.
	a.Billing.RegisterExportGuard(accounting.ExportStopped)
	a.Ledger.Module = m
}

// subscribeP4Accounting registers the event subscribers (one per consumed
// event type; idempotent per event, FR-PST-05).
func (a *App) subscribeP4Accounting() {
	for event, h := range a.Ledger.Module.Subscriptions() {
		a.Bus.Subscribe(event, "accounting.post:"+event, h)
	}
}

// demoP4Accounting seeds the OneClub chart of accounts, the tax codes and
// the default posting rules (idempotent); the book of a property is opened
// by Finance with its cut-over date (Accounting → Setup).
func demoP4Accounting(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	_, err := accounting.SeedChart(ctx, tx)
	return err
}
