package app

// PRD P5 EP-17–20: advanced segmentation & VIP, advanced loyalty, campaign & lifecycle journeys, CRM & sales analytics. internal/app/p5.go calls these functions; the area fills them.

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

// p5CRM holds the services of the area.
type p5CRM struct{}

func p5CRMContributions() []catalog.Contribution   { return nil }
func p5CRMDocumentTypes() []provision.DocumentType { return nil }
func p5CRMTemplates() []provision.Template         { return nil }

// buildP5CRM wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5CRM(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP5CRM registers the event subscribers.
func (a *App) subscribeP5CRM() {}

// demoP5CRM seeds the demo data of the area.
func demoP5CRM(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
