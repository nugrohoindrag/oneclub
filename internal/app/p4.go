package app

// Wiring of PRD P4 Enterprise Back Office (inventory, procurement,
// accounting, CMS) next to P3 (PRD P4 §5.4.1: internal/app is shared and
// changes additively). Each area wires itself in p4_<area>.go.

import (
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// P4 holds the P4 services.
type P4 struct {
	Stock      p4Inventory
	Purchasing p4Procurement
	Ledger     p4Accounting
	Content    p4CMS
}

func p4Contributions() []catalog.Contribution {
	var out []catalog.Contribution
	for _, c := range [][]catalog.Contribution{p4InventoryContributions(), p4ProcurementContributions(), p4AccountingContributions(),
		p4CMSContributions()} {
		out = append(out, c...)
	}
	return out
}

func p4DocumentTypes() []provision.DocumentType {
	var out []provision.DocumentType
	for _, d := range [][]provision.DocumentType{p4InventoryDocumentTypes(), p4ProcurementDocumentTypes(), p4AccountingDocumentTypes(),
		p4CMSDocumentTypes()} {
		out = append(out, d...)
	}
	return out
}

func p4Templates() []provision.Template {
	var out []provision.Template
	for _, t := range [][]provision.Template{p4InventoryTemplates(), p4ProcurementTemplates(), p4AccountingTemplates(), p4CMSTemplates()} {
		out = append(out, t...)
	}
	return out
}

// buildP4 wires the P4 services and routes after P3's.
func (a *App) buildP4(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	a.buildP4Inventory(reg, cfg, db, files)
	a.buildP4Procurement(reg, cfg, db, files)
	a.buildP4Accounting(reg, cfg, db, files)
	a.buildP4CMS(reg, cfg, db, files)
}

// subscribeP4 registers the P4 event subscribers.
func (a *App) subscribeP4() {
	a.subscribeP4Inventory()
	a.subscribeP4Procurement()
	a.subscribeP4Accounting()
	a.subscribeP4CMS()
}
