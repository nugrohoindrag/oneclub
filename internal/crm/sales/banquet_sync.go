package sales

// Banquet pipeline sync (PRD P3 FR-BQT-14): the sales pipeline follows the
// status of the event converted from a quotation. banquet.event_confirmed
// records the confirmation on the opportunity (once); banquet.event_cancelled
// records the cancellation and closes the opportunity as Lost with reason
// "cancelled" — the money the club returns comes back through
// billing.refund_processed, which claws the commission back within the
// clawback window (OnRefund, FR-COM-05). Events are decoded by name and the
// event's quotation is read from the reporting read model
// (reporting.banquet_events): crm never imports banquet.

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/outbox"
)

// Banquet events consumed (docs/p3-p4-contracts.md).
const (
	BanquetEventConfirmed = "banquet.event_confirmed"
	BanquetEventCancelled = "banquet.event_cancelled"
)

// OnBanquetEvent syncs the opportunity of an event's quotation with the
// event status (idempotent per event and status).
func (m *Module) OnBanquetEvent(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		EventID    uuid.UUID  `json:"eventId"`
		Number     string     `json:"number"`
		Title      string     `json:"title"`
		CustomerID *uuid.UUID `json:"customerId"`
		StartDate  string     `json:"startDate"`
	}
	if err := e.Decode(&p); err != nil || p.EventID == uuid.Nil || e.PropertyID == nil {
		return nil //nolint:nilerr // foreign payload
	}
	if e.Type != BanquetEventConfirmed && e.Type != BanquetEventCancelled {
		return nil
	}
	property := *e.PropertyID
	var quotation *string
	err := tx.QueryRow(ctx, `SELECT quotation_number FROM reporting.banquet_events WHERE event_id = $1`, p.EventID).Scan(&quotation)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var oppID *uuid.UUID
	if quotation != nil {
		var qstatus string
		err := tx.QueryRow(ctx, `SELECT opportunity_id, status FROM crm.sales_quotations WHERE property_id = $1 AND number = $2
			ORDER BY (status = 'accepted') DESC, version DESC LIMIT 1`, property, *quotation).Scan(&oppID, &qstatus)
		if err != nil && !dbtx.IsNoRows(err) {
			return err
		}
		if e.Type == BanquetEventCancelled && qstatus != "accepted" {
			return nil // the hold of a rejected / expired quotation: the pipeline follows the quotation itself
		}
	}
	target := activityTarget{opportunity: oppID}
	if oppID == nil {
		if p.CustomerID == nil {
			return nil
		}
		target.customer = p.CustomerID
	}
	ref := e.Type + ":" + p.EventID.String()
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.sales_activities WHERE property_id = $1 AND source = 'system' AND external_ref = $2)`,
		property, ref).Scan(&done); err != nil || done {
		return err
	}
	label := strings.TrimSpace("Event " + p.Number + " · " + p.Title)
	subject, note := label+" confirmed (Definite)", "Event date "+p.StartDate
	if e.Type == BanquetEventCancelled {
		subject, note = label+" cancelled", "The event of "+deref(quotation)+" was cancelled"
	}
	if _, err := m.addActivity(ctx, tx, property, target, ActivityInput{Type: "note", Direction: "internal", Subject: subject, Notes: note},
		"system", ref); err != nil {
		return err
	}
	if e.Type != BanquetEventCancelled || oppID == nil {
		return nil
	}
	o, err := lockOpportunity(ctx, tx, property, *oppID)
	if err != nil {
		return err
	}
	if o.Status == "lost" {
		return nil
	}
	return m.loseCancelledEvent(ctx, tx, property, o, "event "+p.Number+" cancelled")
}

// loseCancelledEvent closes an open or won opportunity as Lost (reason
// cancelled) when its event is cancelled.
func (m *Module) loseCancelledEvent(ctx context.Context, tx pgx.Tx, property uuid.UUID, o Opportunity, note string) error {
	st, err := stageOfKind(ctx, tx, o.PipelineID, "lost")
	if err != nil {
		return err
	}
	if err := m.changeStage(ctx, tx, property, o, st, "cancelled "+note); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_opportunities SET status = 'lost', lost_at = now(), lost_reason = 'cancelled', lost_note = $2 WHERE id = $1`,
		o.ID, note); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "lose", EntityType: "crm.opportunity", EntityID: o.ID.String(),
		EntityLabel: o.Number + " · " + o.Title, PropertyID: &property, Reason: "cancelled " + note, ActorName: "event:" + BanquetEventCancelled,
		Before: map[string]any{"status": o.Status}, After: map[string]any{"status": "lost", "reason": "cancelled"}}); err != nil {
		return err
	}
	_, err = m.Events.Publish(ctx, tx, EventOpportunityLost, "crm.opportunity", &o.ID, &property, map[string]any{"opportunityId": o.ID,
		"number": o.Number, "line": o.Line, "reason": "cancelled", "customerId": o.CustomerID, "ownerUserId": o.OwnerUserID, "wasWon": o.Status == "won"})
	return err
}
