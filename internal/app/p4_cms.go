package app

// PRD P4 EP-24 Landing Page & CMS (owner: cms) and EP-25 Integration Layer
// P4 (additive adapters and admin routes of platform/integration).
// internal/app/p3.go and p4.go call these functions.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/cms"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// p4CMS holds the services of the area.
type p4CMS struct {
	CMS *cms.Module
}

// p4CMSContributions are the catalogue contributions (permissions, role mappings).
func p4CMSContributions() []catalog.Contribution {
	return []catalog.Contribution{cms.Contribution()}
}

// p4CMSDocumentTypes are the approval document types.
func p4CMSDocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{cms.ContentDocumentType}
}

// p4CMSTemplates are the notification templates.
func p4CMSTemplates() []provision.Template { return cms.Templates() }

// buildP4CMS wires routes, hooks, approval decisions and jobs.
func (a *App) buildP4CMS(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	m := &cms.Module{DB: db, Events: a.Bus, Approvals: a.Approvals, Files: files, Notify: a.Notification, Cfg: cfg}
	if db != nil && a.Instance != nil {
		m.Enabled = a.Instance.ModuleEnabled
	}
	m.Register(reg, a.Engine)
	a.Approvals.RegisterDocumentType(cms.ContentDocumentType, m.Decision)
	m.RegisterJobs(a.Registrar)
	a.Content.CMS = m

	// EP-25: payment routing and settlement per method, the e-mail sender
	// domain check and the hardware profiles of the bridge agent.
	(&integration.P4HTTP{Svc: a.Integrations}).Register(reg)
}

// subscribeP4CMS registers the event subscribers: every publication and
// website change asks the website to revalidate its cache (FR-CMS-10)
// through the website integration (best effort: a failed call is in the
// Integration Logs; the website also revalidates by time and by the
// revision of /public/cms/site, and Marketing can purge the cache).
func (a *App) subscribeP4CMS() {
	for _, ev := range cms.EventTypes() {
		a.Bus.Subscribe(ev, "integration.website_revalidate", a.revalidateWebsite)
	}
}

func (a *App) revalidateWebsite(ctx context.Context, _ pgx.Tx, e outbox.Event) error {
	var p cms.SiteChanged
	if err := e.Decode(&p); err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	w, code, err := a.Integrations.Website(ctx)
	if errors.Is(err, integration.ErrNotConfigured) {
		return nil
	}
	if err != nil {
		slog.WarnContext(ctx, "website revalidation unavailable", "err", err)
		return nil
	}
	if err := w.Revalidate(ctx, integration.RevalidateRequest{PropertyID: p.PropertyID.String(), Revision: p.Revision, Paths: p.Paths,
		Tags: []string{"cms"}, Event: e.Type}); err != nil {
		slog.WarnContext(ctx, "website revalidation failed", "integration", code, "err", err)
	}
	return nil
}

// demoP4CMS seeds demo data for the area on the MAIN property (idempotent).
func demoP4CMS(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	return cms.SeedDemo(ctx, tx, property)
}
