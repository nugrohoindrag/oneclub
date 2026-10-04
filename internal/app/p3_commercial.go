package app

// PRD P3 EP-10–11 Pricing & Promotion Engine and Package Management (owner: commercial).
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

// p3Commercial holds the services of the area.
type p3Commercial struct{}

// p3CommercialContributions are the catalogue contributions (permissions, role mappings).
func p3CommercialContributions() []catalog.Contribution { return nil }

// p3CommercialDocumentTypes are the approval document types.
func p3CommercialDocumentTypes() []provision.DocumentType { return nil }

// p3CommercialTemplates are the notification templates.
func p3CommercialTemplates() []provision.Template { return nil }

// buildP3Commercial wires routes, hooks, approval decisions and jobs.
func (a *App) buildP3Commercial(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP3Commercial registers the event subscribers.
func (a *App) subscribeP3Commercial() {}

// demoP3Commercial seeds demo data for the area on the MAIN property (idempotent).
func demoP3Commercial(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
