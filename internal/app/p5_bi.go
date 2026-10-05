package app

// PRD P5 EP-21 and EP-27: analytics store, executive dashboard with targets, drill-down, scheduled & self-service reports, HR KPI & reports. internal/app/p5.go calls these functions; the area fills them.

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

// p5BI holds the services of the area.
type p5BI struct{}

func p5BIContributions() []catalog.Contribution   { return nil }
func p5BIDocumentTypes() []provision.DocumentType { return nil }
func p5BITemplates() []provision.Template         { return nil }

// buildP5BI wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5BI(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {}

// subscribeP5BI registers the event subscribers.
func (a *App) subscribeP5BI() {}

// demoP5BI seeds the demo data of the area.
func demoP5BI(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
