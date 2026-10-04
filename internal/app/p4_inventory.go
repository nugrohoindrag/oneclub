package app

// PRD P4 EP-01–09 Inventory: item master, stock movement & balance, requisition & transfer, opname, valuation & COGS, automatic consumption, production/waste/batch/expiry, replenishment, assets (owner: inventory).
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

// p4Inventory holds the services of the area.
type p4Inventory struct{}

// p4InventoryContributions are the catalogue contributions (permissions, role mappings).
func p4InventoryContributions() []catalog.Contribution { return nil }

// p4InventoryDocumentTypes are the approval document types.
func p4InventoryDocumentTypes() []provision.DocumentType { return nil }

// p4InventoryTemplates are the notification templates.
func p4InventoryTemplates() []provision.Template { return nil }

// buildP4Inventory wires routes, hooks, approval decisions and jobs.
func (a *App) buildP4Inventory(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP4Inventory registers the event subscribers.
func (a *App) subscribeP4Inventory() {}

// demoP4Inventory seeds demo data for the area on the MAIN property (idempotent).
func demoP4Inventory(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
