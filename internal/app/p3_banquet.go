package app

// PRD P3 EP-12–15 Event Management, Banquet/MICE/Wedding, BEO, Banquet Venue & Event Resource (owner: banquet).
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

// p3Banquet holds the services of the area.
type p3Banquet struct{}

// p3BanquetContributions are the catalogue contributions (permissions, role mappings).
func p3BanquetContributions() []catalog.Contribution { return nil }

// p3BanquetDocumentTypes are the approval document types.
func p3BanquetDocumentTypes() []provision.DocumentType { return nil }

// p3BanquetTemplates are the notification templates.
func p3BanquetTemplates() []provision.Template { return nil }

// buildP3Banquet wires routes, hooks, approval decisions and jobs.
func (a *App) buildP3Banquet(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP3Banquet registers the event subscribers.
func (a *App) subscribeP3Banquet() {}

// demoP3Banquet seeds demo data for the area on the MAIN property (idempotent).
func demoP3Banquet(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
