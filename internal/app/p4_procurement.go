package app

// PRD P4 EP-10–15 Procurement: suppliers & vendor performance, purchase requisition, RFQ & vendor quotation, purchase order, goods receipt & return, vendor invoice & 3-way matching (owner: procurement).
// Wiring stub filled in by the area; internal/app/p3.go and p4.go call it.

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

// p4Procurement holds the services of the area.
type p4Procurement struct{}

// p4ProcurementContributions are the catalogue contributions (permissions, role mappings).
func p4ProcurementContributions() []catalog.Contribution { return nil }

// p4ProcurementDocumentTypes are the approval document types.
func p4ProcurementDocumentTypes() []provision.DocumentType { return nil }

// p4ProcurementTemplates are the notification templates.
func p4ProcurementTemplates() []provision.Template { return nil }

// buildP4Procurement wires routes, hooks, approval decisions and jobs.
func (a *App) buildP4Procurement(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP4Procurement registers the event subscribers.
func (a *App) subscribeP4Procurement() {}

// demoP4Procurement seeds demo data for the area on the MAIN property (idempotent).
func demoP4Procurement(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
