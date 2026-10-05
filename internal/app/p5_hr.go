package app

// PRD P5 EP-01–05, EP-16, EP-24: core HR (organization, employees, contracts, documents, recruitment, training & certification, performance review, Employee Self Service, HR & Workforce Policies). internal/app/p5.go calls these functions; the area fills them.

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

// p5HR holds the services of the area.
type p5HR struct{}

func p5HRContributions() []catalog.Contribution   { return nil }
func p5HRDocumentTypes() []provision.DocumentType { return nil }
func p5HRTemplates() []provision.Template         { return nil }

// buildP5HR wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5HR(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {}

// subscribeP5HR registers the event subscribers.
func (a *App) subscribeP5HR() {}

// demoP5HR seeds the demo data of the area.
func demoP5HR(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
