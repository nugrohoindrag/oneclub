package app

// PRD P4 EP-24 Landing Page & CMS (owner: cms).
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

// p4CMS holds the services of the area.
type p4CMS struct{}

// p4CMSContributions are the catalogue contributions (permissions, role mappings).
func p4CMSContributions() []catalog.Contribution { return nil }

// p4CMSDocumentTypes are the approval document types.
func p4CMSDocumentTypes() []provision.DocumentType { return nil }

// p4CMSTemplates are the notification templates.
func p4CMSTemplates() []provision.Template { return nil }

// buildP4CMS wires routes, hooks, approval decisions and jobs.
func (a *App) buildP4CMS(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP4CMS registers the event subscribers.
func (a *App) subscribeP4CMS() {}

// demoP4CMS seeds demo data for the area on the MAIN property (idempotent).
func demoP4CMS(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
