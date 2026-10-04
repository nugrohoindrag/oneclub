package app

// PRD P3 EP-05–09 Customer 360 & segmentation, campaigns & reminders, feedback/NPS/complaint tickets, Top Spender, Loyalty (owner: crm).
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

// p3Engagement holds the services of the area.
type p3Engagement struct{}

// p3EngagementContributions are the catalogue contributions (permissions, role mappings).
func p3EngagementContributions() []catalog.Contribution { return nil }

// p3EngagementDocumentTypes are the approval document types.
func p3EngagementDocumentTypes() []provision.DocumentType { return nil }

// p3EngagementTemplates are the notification templates.
func p3EngagementTemplates() []provision.Template { return nil }

// buildP3Engagement wires routes, hooks, approval decisions and jobs.
func (a *App) buildP3Engagement(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP3Engagement registers the event subscribers.
func (a *App) subscribeP3Engagement() {}

// demoP3Engagement seeds demo data for the area on the MAIN property (idempotent).
func demoP3Engagement(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
