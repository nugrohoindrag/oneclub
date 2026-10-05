package app

// PRD P4 EP-10–15 Procurement: suppliers & vendor performance, purchase requisition, RFQ & vendor quotation, purchase order, goods receipt & return, vendor invoice & 3-way matching (owner: procurement).
// Wiring stub filled in by the area; internal/app/p3.go and p4.go call it.

import (
	"context"
	"oneclub/internal/accounting"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
	"oneclub/internal/procurement"
)

// p4Procurement holds the services of the area.
type p4Procurement struct {
	Procurement *procurement.Module
}

// p4ProcurementContributions are the catalogue contributions (permissions, role mappings).
func p4ProcurementContributions() []catalog.Contribution {
	return []catalog.Contribution{procurement.P4Contribution()}
}

// p4ProcurementDocumentTypes are the approval document types.
func p4ProcurementDocumentTypes() []provision.DocumentType { return procurement.DocumentTypes }

// p4ProcurementTemplates are the notification templates.
func p4ProcurementTemplates() []provision.Template { return procurement.Templates() }

// buildP4Procurement wires routes, hooks, approval decisions and jobs.
func (a *App) buildP4Procurement(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	m := &procurement.Module{DB: db, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification,
		PublicURL: func() string { return cfg.PublicBaseURL }, WebsiteURL: func() string { return cfg.WebsiteURL }}
	a.Purchasing.Procurement = m
	m.Register(reg, a.Engine)
	// FR-GR-01: delivery note photo / document of goods receipts (platform files).
	m.RegisterGoodsReceiptAttachments(reg, files)
	hooks := m.Decisions()
	for _, dt := range procurement.DocumentTypes {
		a.Approvals.RegisterDocumentType(dt, hooks[dt.Code])
	}
	procurement.RegisterJobs(a.Registrar, db, a.Instance.Location)
	// FR-OPS-P4-04: goods receipts recorded offline on a warehouse device.
	a.Sync.Handle(procurement.SyncGoodsReceiptAction, m.SyncGoodsReceipt)
	// K10: documents dated in a closed accounting period are refused.
	m.SetPeriodGuard(accounting.PeriodStatus)
}

// subscribeP4Procurement registers the event subscribers.
func (a *App) subscribeP4Procurement() {
	m := a.Purchasing.Procurement
	// EP-08: automatic purchase requisition from the reorder job.
	a.Bus.Subscribe(procurement.EventReorderNeeded, "procurement.auto_requisition", m.ReorderSubscriber)
	// K1: banquet material requisition from the BEO; a revision replaces the open lines.
	a.Bus.Subscribe(procurement.EventBEOIssued, "procurement.banquet_requisition", m.BEOSubscriber(false))
	a.Bus.Subscribe(procurement.EventBEORevised, "procurement.banquet_requisition_revised", m.BEOSubscriber(true))
	// Vendor invoice payment status from accounting (Partially Paid / Paid).
	a.Bus.Subscribe(procurement.EventVendorPaymentMade, "procurement.vendor_invoice_payment", m.PaymentSubscriber)
}

// demoP4Procurement seeds demo data for the area on the MAIN property (idempotent).
func demoP4Procurement(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	return procurement.SeedDemo(ctx, tx, property)
}
