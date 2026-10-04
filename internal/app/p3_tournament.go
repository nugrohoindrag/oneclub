package app

// PRD P3 EP-16 Tournament Management (owner: golf/tournament).
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

// p3Tournament holds the services of the area.
type p3Tournament struct{}

// p3TournamentContributions are the catalogue contributions (permissions, role mappings).
func p3TournamentContributions() []catalog.Contribution { return nil }

// p3TournamentDocumentTypes are the approval document types.
func p3TournamentDocumentTypes() []provision.DocumentType { return nil }

// p3TournamentTemplates are the notification templates.
func p3TournamentTemplates() []provision.Template { return nil }

// buildP3Tournament wires routes, hooks, approval decisions and jobs.
func (a *App) buildP3Tournament(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP3Tournament registers the event subscribers.
func (a *App) subscribeP3Tournament() {}

// demoP3Tournament seeds demo data for the area on the MAIN property (idempotent).
func demoP3Tournament(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
