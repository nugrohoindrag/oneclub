package pos

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// FolioOrders lists the orders charged to the given folios (e.g. the
// on-course orders of a golf booking: the caddy tablet's order history,
// demo feedback 10 Oct 2026 #32), latest first.
func (m *Module) FolioOrders(ctx context.Context, q dbtx.Querier, folios []uuid.UUID) ([]Order, error) {
	list, err := handle.List[Order](q.Query(ctx, orderSelect+` WHERE o.charge_folio_id = ANY($1) ORDER BY o.created_at DESC LIMIT 50`, folios))
	if err != nil {
		return list, err
	}
	for i := range list {
		if list[i].Lines, err = handle.List[OrderLine](q.Query(ctx, orderLineSelect+` WHERE order_id = $1 ORDER BY line_no`, list[i].ID)); err != nil {
			return list, err
		}
	}
	return list, nil
}

// CancelOrder cancels an order charged to a folio while the kitchen has
// not started it: the folio charges are voided and the tickets cancelled.
func (m *Module) CancelOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID, reason string) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status == "voided" {
		return o, nil
	}
	if o.Status != "charged" && o.Status != "open" {
		return o, errs.Conflict("order_closed", "the order is "+o.Status+": refund it at the POS")
	}
	if o.ServiceStatus != "new" && o.ServiceStatus != "sent" {
		return o, errs.Conflict("order_in_progress", "the tee house already started the order ("+o.ServiceStatus+"): call the outlet")
	}
	var started bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.kitchen_tickets WHERE order_id = $1 AND status NOT IN ('received', 'cancelled'))`, oid).Scan(&started); err != nil {
		return o, err
	}
	if started {
		return o, errs.Conflict("order_in_progress", "the tee house already started the order: call the outlet")
	}
	for _, l := range o.Lines {
		if l.Status != "active" || l.ChargedFolioID == nil {
			continue
		}
		if _, err := m.Billing.VoidSourceCharges(ctx, tx, "commercial.order_line", l.ID, "order cancelled: "+reason); err != nil {
			return o, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.order_lines SET status = 'voided', void_reason = $2 WHERE order_id = $1 AND status = 'active'`, oid, reason); err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.kitchen_tickets SET status = 'cancelled' WHERE order_id = $1 AND status <> 'served'`, oid); err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET status = 'voided', void_reason = $2 WHERE id = $1`, oid, reason); err != nil {
		return o, err
	}
	after, err := m.Order(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	if err := publishRT(ctx, tx, "kds", o.PropertyID, "order_changed", oid.String(), nil); err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionVoid, EntityType: "commercial.order",
		EntityID: oid.String(), EntityLabel: o.OrderNo, PropertyID: &o.PropertyID, Before: o, After: after, Reason: reason})
}
