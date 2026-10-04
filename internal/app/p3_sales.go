package app

// PRD P3 EP-01–04 CRM Sales: leads, pipelines, opportunities, quotations, sales targets & commission (owner: crm).
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

// p3Sales holds the services of the area.
type p3Sales struct{}

// p3SalesContributions are the catalogue contributions (permissions, role mappings).
func p3SalesContributions() []catalog.Contribution { return nil }

// p3SalesDocumentTypes are the approval document types.
func p3SalesDocumentTypes() []provision.DocumentType { return nil }

// p3SalesTemplates are the notification templates.
func p3SalesTemplates() []provision.Template { return nil }

// buildP3Sales wires routes, hooks, approval decisions and jobs.
func (a *App) buildP3Sales(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP3Sales registers the event subscribers.
func (a *App) subscribeP3Sales() {}

// demoP3Sales seeds demo data for the area on the MAIN property (idempotent).
func demoP3Sales(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
