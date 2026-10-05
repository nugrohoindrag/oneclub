package app

// PRD P5 EP-06–08: shift scheduling, attendance (kiosk, devices, geofence) and leave, permission & overtime. internal/app/p5.go calls these functions; the area fills them.

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

// p5HRTime holds the services of the area.
type p5HRTime struct{}

func p5HRTimeContributions() []catalog.Contribution   { return nil }
func p5HRTimeDocumentTypes() []provision.DocumentType { return nil }
func p5HRTimeTemplates() []provision.Template         { return nil }

// buildP5HRTime wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5HRTime(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP5HRTime registers the event subscribers.
func (a *App) subscribeP5HRTime() {}

// demoP5HRTime seeds the demo data of the area.
func demoP5HRTime(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
