package app

// PRD P5 EP-22–23: advanced package management (multi-business, capacity & time blocks, profitability) and advanced tournament (team formats, series & order of merit, history, federation). internal/app/p5.go calls these functions; the area fills them.

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

// p5Leisure holds the services of the area.
type p5Leisure struct{}

func p5LeisureContributions() []catalog.Contribution   { return nil }
func p5LeisureDocumentTypes() []provision.DocumentType { return nil }
func p5LeisureTemplates() []provision.Template         { return nil }

// buildP5Leisure wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5Leisure(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP5Leisure registers the event subscribers.
func (a *App) subscribeP5Leisure() {}

// demoP5Leisure seeds the demo data of the area.
func demoP5Leisure(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
