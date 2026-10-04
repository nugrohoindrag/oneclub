package app

// PRD P4 EP-16–23 Accounting: general ledger, automatic posting, AR, AP, cash & bank, revenue & tax, financial reports, transition & opening balances (owner: accounting).
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

// p4Accounting holds the services of the area.
type p4Accounting struct{}

// p4AccountingContributions are the catalogue contributions (permissions, role mappings).
func p4AccountingContributions() []catalog.Contribution { return nil }

// p4AccountingDocumentTypes are the approval document types.
func p4AccountingDocumentTypes() []provision.DocumentType { return nil }

// p4AccountingTemplates are the notification templates.
func p4AccountingTemplates() []provision.Template { return nil }

// buildP4Accounting wires routes, hooks, approval decisions and jobs.
func (a *App) buildP4Accounting(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
}

// subscribeP4Accounting registers the event subscribers.
func (a *App) subscribeP4Accounting() {}

// demoP4Accounting seeds demo data for the area on the MAIN property (idempotent).
func demoP4Accounting(ctx context.Context, tx pgx.Tx, property uuid.UUID) error { return nil }
