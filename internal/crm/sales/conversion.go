package sales

// Deal money of membership quotations (FR-QUO-06, PRD P3 §16 #4): an
// accepted membership quotation becomes a Membership Application (the
// membership module records the quotation on it); the application is
// activated only once its fee is paid or waived, so membership.activated
// pays the deal in full and recognises the commission. Decoded by name
// through the reporting read model — crm never imports membership.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/outbox"
)

// MembershipActivated is the membership event that pays membership deals.
const MembershipActivated = "membership.activated"

// OnMembershipActivated pays the deal of an application converted from a
// quotation (idempotent per membership).
func (m *Module) OnMembershipActivated(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		ApplicationID uuid.UUID `json:"applicationId"`
		MembershipID  uuid.UUID `json:"membershipId"`
	}
	if err := e.Decode(&p); err != nil || p.ApplicationID == uuid.Nil || p.MembershipID == uuid.Nil || e.PropertyID == nil {
		return nil //nolint:nilerr // foreign payload
	}
	property := *e.PropertyID
	var qid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT quotation_id FROM reporting.sales_membership_applications WHERE application_id = $1`, p.ApplicationID).Scan(&qid)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	q, err := lockQuotation(ctx, tx, property, qid)
	if err != nil {
		return err
	}
	if q.Status != "accepted" || q.PaidAt != nil {
		return nil
	}
	amount := dec(q.Total).Sub(dec(q.PaidAmount))
	if amount.IsNegative() {
		amount = dec("0")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO crm.sales_deal_payments (id, property_id, quotation_id, source_type, source_id, amount, occurred_at)
		VALUES ($1,$2,$3,'manual',$4,$5::numeric,$6) ON CONFLICT (source_type, source_id) DO NOTHING`, id.New(), property, qid, p.MembershipID,
		amount.String(), e.OccurredAt)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET paid_amount = paid_amount + $2::numeric WHERE id = $1`, qid, amount.String()); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "deal_payment", EntityType: "crm.quotation", EntityID: qid.String(),
		EntityLabel: q.Number + " v" + itoa(q.Version), PropertyID: &property, ActorName: "event:" + MembershipActivated,
		After: map[string]any{"sourceType": "membership", "membershipId": p.MembershipID, "applicationId": p.ApplicationID, "amount": amount.String()}}); err != nil {
		return err
	}
	return m.evaluateDeal(ctx, tx, property, qid, e.OccurredAt)
}
