package app

// Wiring of PRD P3 Commercial & Business Expansion next to P1 and P2 (Tech
// Doc §12.3 #5: the composition root, the catalogue and the navigation
// change additively). P3 modules use the earlier modules through their
// public API, registered hooks and domain events. Each area wires itself in
// its own file (p3_<area>.go) so the areas change independently (PRD P3
// §5.4.1 ownership per sub-package).

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
	"oneclub/internal/reporting"
)

// p3Contributions are the catalogue contributions of P3 and P4.
func p3Contributions() []catalog.Contribution {
	out := []catalog.Contribution{billing.P3Contribution(), reporting.P3P4Contribution()}
	for _, c := range [][]catalog.Contribution{p3SalesContributions(), p3EngagementContributions(), p3CommercialContributions(),
		p3BanquetContributions(), p3TournamentContributions(), p4Contributions()} {
		out = append(out, c...)
	}
	return out
}

// p3DocumentTypes are the approval document types of P3 and P4.
func p3DocumentTypes() []provision.DocumentType {
	var out []provision.DocumentType
	out = append(out, billing.P3DocumentTypes...)
	for _, d := range [][]provision.DocumentType{p3SalesDocumentTypes(), p3EngagementDocumentTypes(), p3CommercialDocumentTypes(),
		p3BanquetDocumentTypes(), p3TournamentDocumentTypes(), p4DocumentTypes()} {
		out = append(out, d...)
	}
	return out
}

// p3Templates are the notification templates of the P3 and P4 modules.
func p3Templates() []provision.Template {
	var out []provision.Template
	out = append(out, billing.P3Templates()...)
	for _, t := range [][]provision.Template{p3SalesTemplates(), p3EngagementTemplates(), p3CommercialTemplates(), p3BanquetTemplates(),
		p3TournamentTemplates(), p4Templates()} {
		out = append(out, t...)
	}
	return out
}

// P3 holds the P3 services.
type P3 struct {
	BillingHTTP *billing.HTTP
	Sales       p3Sales
	Engage      p3Engagement
	Promo       p3Commercial
	Banquet     p3Banquet
	Tournament  p3Tournament
}

// buildP3 wires the P3 services and routes after P2's, then P4's.
func (a *App) buildP3(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files, billingHTTP *billing.HTTP) {
	a.BillingHTTP = billingHTTP
	billingHTTP.WebsiteURL = func() string { return cfg.WebsiteURL }
	billingHTTP.RegisterP3(reg)
	a.Approvals.RegisterDocumentType(billing.WriteOffDocumentType, billingHTTP.WriteOffDecision)
	a.Approvals.RegisterDocumentType(billing.CreditOverrideDocumentType, billingHTTP.CreditOverrideDecision)
	a.Approvals.RegisterDocumentType(billing.ReopenDocumentType, billingHTTP.ReopenDecision)
	billingHTTP.RegisterP3Jobs(a.Registrar, a.Instance.Location)
	a.Reporting.RegisterP3P4(reg)

	a.buildP3Sales(reg, cfg, db, files)
	a.buildP3Engagement(reg, cfg, db, files)
	a.buildP3Commercial(reg, cfg, db, files)
	a.buildP3Banquet(reg, cfg, db, files)
	a.buildP3Tournament(reg, cfg, db, files)
	a.buildP4(reg, cfg, db, files)
}

// subscribeP3 registers the P3 (and P4) event subscribers.
func (a *App) subscribeP3() {
	// Gateway payments of invoice / schedule payment links are allocated
	// once settled (FR-INT-P3-04).
	a.Bus.Subscribe(billing.EventPaymentSettled, "billing.allocate_settled", func(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
		var p billing.SettledPayload
		if err := e.Decode(&p); err != nil {
			return err
		}
		if err := a.Billing.OnPaymentSettled(ctx, tx, p.PaymentID); err != nil {
			return err
		}
		return a.Billing.OnSchedulePaymentSettled(ctx, tx, p.PaymentID)
	})
	a.subscribeP3Sales()
	a.subscribeP3Engagement()
	a.subscribeP3Commercial()
	a.subscribeP3Banquet()
	a.subscribeP3Tournament()
	a.subscribeP4()
}

// seedP3P4Demo seeds the demo data of the P3 and P4 areas.
func seedP3P4Demo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	for _, f := range []func(context.Context, pgx.Tx, uuid.UUID) error{demoP3Sales, demoP3Engagement, demoP3Commercial, demoP3Banquet,
		demoP3Tournament, demoP4Inventory, demoP4Procurement, demoP4Accounting, demoP4CMS} {
		if err := f(ctx, tx, property); err != nil {
			return err
		}
	}
	return nil
}
