package billing

// PRD P3 FR-TKT-05: an approved complaint compensation of type refund is
// paid back by Billing (crm.ticket_compensation_approved, decoded by name —
// billing cannot import crm's engagement package). The refund goes to the
// original method of the customer's latest completed payment that can carry
// it; the compensation approval replaces the Refund Policy approval. Once
// per compensation.

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/outbox"
)

// EventCompensationApproved is the CRM complaint compensation approval.
const EventCompensationApproved = "crm.ticket_compensation_approved"

// OnCompensationApproved refunds an approved refund compensation.
func (s *Service) OnCompensationApproved(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		CompensationID uuid.UUID  `json:"compensationId"`
		Number         string     `json:"number"`
		TicketNumber   string     `json:"ticketNumber"`
		CustomerID     *uuid.UUID `json:"customerId"`
		Type           string     `json:"type"`
		Amount         *string    `json:"amount"`
	}
	if err := e.Decode(&p); err != nil || e.PropertyID == nil || p.Type != "refund" || p.CustomerID == nil || p.Amount == nil {
		return nil //nolint:nilerr // another shape or no refund
	}
	amount, err := decimal.NewFromString(*p.Amount)
	if err != nil || !amount.IsPositive() {
		return nil //nolint:nilerr // nothing to refund
	}
	property := *e.PropertyID
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	prefix := "Complaint compensation " + p.Number + " · "
	reason := prefix + "ticket " + p.TicketNumber
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing.refunds WHERE property_id = $1 AND starts_with(reason, $2) AND status <> 'cancelled')`,
		property, prefix).Scan(&done); err != nil || done {
		return err
	}
	var pid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT p.id FROM billing.payments p LEFT JOIN billing.folios f ON f.id = p.folio_id
		LEFT JOIN billing.customer_accounts a ON a.id = p.account_id
		WHERE p.property_id = $1 AND p.status IN ('completed', 'refunded') AND coalesce(f.customer_id, a.customer_id) = $2
		AND p.method_type NOT IN ('loyalty_points', 'folio_transfer', 'voucher_prepaid')
		AND p.amount - p.refunded_amount - coalesce((SELECT sum(r.amount) FROM billing.refunds r WHERE r.payment_id = p.id AND r.status = 'pending'), 0) >= $3::numeric
		ORDER BY p.created_at DESC, p.id LIMIT 1`, property, *p.CustomerID, amount.String()).Scan(&pid)
	if dbtx.IsNoRows(err) {
		slog.WarnContext(ctx, "compensation refund needs a manual refund: no refundable payment", "compensation", p.Number, "ticket", p.TicketNumber)
		return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "compensation_refund_pending", EntityType: "crm.ticket_compensation",
			EntityID: p.CompensationID.String(), EntityLabel: p.Number, PropertyID: &property, Reason: reason,
			After: map[string]any{"amount": amount.String(), "refunded": false}, Metadata: map[string]any{"event": e.ID}})
	}
	if err != nil {
		return err
	}
	r, err := s.RequestRefund(ctx, tx, pid, amount, "original_method", reason, nil)
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "compensation_refund", EntityType: "crm.ticket_compensation",
		EntityID: p.CompensationID.String(), EntityLabel: p.Number, PropertyID: &property, Reason: reason,
		After: map[string]any{"refund": r.Number, "amount": amount.String(), "paymentId": pid}, Metadata: map[string]any{"event": e.ID}})
}
