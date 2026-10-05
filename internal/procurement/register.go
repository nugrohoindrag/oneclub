package procurement

// Route registration and the event subscribers of procurement (wired by
// internal/app). Subscribers run inside the outbox dispatch transaction
// with their (subscriber, event) dedupe marker; each is also idempotent on
// its own (reorder: on-order quantities; BEO: version; payments: payment
// and invoice key).

import (
	"context"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/resource"
)

// Register adds the PRD P4 procurement routes. The P0 supplier resource
// (procurement.Suppliers) is registered by the composition root.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	m.registerSuppliers(reg, eng)
	m.registerRequisitions(reg)
	m.registerRFQs(reg)
	m.registerOrders(reg)
	m.registerReceipts(reg)
	m.registerInvoices(reg)
	m.registerPerformance(reg)
	m.registerPublic(reg)
	m.registerMigration(reg)
}

// ReorderSubscriber handles inventory.reorder_needed (EP-08).
func (m *Module) ReorderSubscriber(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	if e.PropertyID == nil {
		return nil
	}
	var p ReorderPayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	_, err := m.OnReorderNeeded(ctx, tx, *e.PropertyID, p)
	return err
}

// BEOSubscriber handles banquet.beo_issued / banquet.beo_revised (K1).
func (m *Module) BEOSubscriber(revised bool) outbox.Handler {
	return func(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
		if e.PropertyID == nil {
			return nil
		}
		var p BEOPayload
		if err := e.Decode(&p); err != nil {
			return err
		}
		_, err := m.OnBEO(ctx, tx, *e.PropertyID, p, revised)
		return err
	}
}

// PaymentSubscriber handles accounting.vendor_payment_made.
func (m *Module) PaymentSubscriber(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	if e.PropertyID == nil {
		return nil
	}
	var p PaymentPayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	return m.OnVendorPaymentMade(ctx, tx, *e.PropertyID, p)
}
