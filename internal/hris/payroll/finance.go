package payroll

// Payroll → Finance & Accounting status (HRIS improvement phase B, spec §19
// and §34 Payroll "Posting Failed"): the run remembers the outbox events it
// published (hris.payroll_posted / hris.payroll_paid) and follows their
// outcome from accounting's own events — accounting.journal_posted with the
// run as source (Posted to Finance) and accounting.posting_exception for one
// of its events (Posting Failed, with the reason). A repost that books a new
// journal clears the failure; a journal booked together with the exception
// (part of the lines missing a mapping) does not. Payroll never reads the
// accounting schema.

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/outbox"
)

// Accounting events followed by payroll.
const (
	evJournalPosted    = "accounting.journal_posted"
	evPostingException = "accounting.posting_exception"
)

// Subscriptions are the event handlers of payroll.
func (m *Module) Subscriptions() map[string]outbox.Handler {
	return map[string]outbox.Handler{evJournalPosted: m.onJournalPosted, evPostingException: m.onPostingException}
}

// financePending publishes an event of the run and remembers its id in col
// (posting_event_id | payment_event_id): the run waits for Accounting.
func (m *Module) financePending(ctx context.Context, tx pgx.Tx, rid uuid.UUID, col string, publish func() (uuid.UUID, error)) error {
	ev, err := publish()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE hris.payroll_runs SET `+col+` = $2, finance_status = CASE WHEN finance_status = 'failed' THEN 'failed' ELSE 'pending' END,
		finance_updated_at = now() WHERE id = $1`, rid, ev)
	return err
}

func (m *Module) onJournalPosted(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		JournalID  uuid.UUID `json:"journalId"`
		Number     string    `json:"number"`
		SourceType string    `json:"sourceType"`
		SourceID   string    `json:"sourceId"`
	}
	if err := e.Decode(&p); err != nil || (p.SourceType != hris.EventPayrollPosted && p.SourceType != hris.EventPayrollPaid) {
		return nil
	}
	rid, err := uuid.Parse(p.SourceID)
	if err != nil {
		return nil
	}
	var status string
	var journals []string
	var failedEvent *string
	var failedJournal *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT finance_status, finance_journals, finance_failed_event, finance_failed_journal FROM hris.payroll_runs WHERE id = $1 FOR UPDATE`,
		rid).Scan(&status, &journals, &failedEvent, &failedJournal)
	if dbtx.IsNoRows(err) {
		return nil // not a run of this instance (or deleted)
	}
	if err != nil {
		return err
	}
	if !slices.Contains(journals, p.Number) {
		journals = append(journals, p.Number)
	}
	next := "posted"
	// the journal of the failed event clears it, unless it was booked with the exception
	if status == "failed" && (failedEvent == nil || *failedEvent != p.SourceType || (failedJournal != nil && *failedJournal == p.JournalID)) {
		next = "failed"
	}
	if next == "posted" {
		_, err = tx.Exec(ctx, `UPDATE hris.payroll_runs SET finance_status = 'posted', finance_journals = $2, finance_message = NULL, finance_failed_event = NULL,
			finance_failed_journal = NULL, finance_updated_at = now() WHERE id = $1`, rid, journals)
	} else {
		_, err = tx.Exec(ctx, `UPDATE hris.payroll_runs SET finance_journals = $2, finance_updated_at = now() WHERE id = $1`, rid, journals)
	}
	if err != nil || status == next {
		return err
	}
	return m.financeAudit(ctx, tx, rid, status, next, "journal "+p.Number)
}

func (m *Module) onPostingException(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		EventID   uuid.UUID  `json:"eventId"`
		EventType string     `json:"eventType"`
		Reason    string     `json:"reason"`
		Message   string     `json:"message"`
		JournalID *uuid.UUID `json:"journalId"`
	}
	if err := e.Decode(&p); err != nil || (p.EventType != hris.EventPayrollPosted && p.EventType != hris.EventPayrollPaid) {
		return nil
	}
	var rid uuid.UUID
	var status string
	if err := tx.QueryRow(ctx, `SELECT id, finance_status FROM hris.payroll_runs WHERE posting_event_id = $1 OR payment_event_id = $1 FOR UPDATE`,
		p.EventID).Scan(&rid, &status); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	msg := p.Reason
	if p.Message != "" {
		msg += ": " + p.Message
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET finance_status = 'failed', finance_message = $2, finance_failed_event = $3,
		finance_failed_journal = $4, finance_updated_at = now() WHERE id = $1`, rid, msg, p.EventType, p.JournalID); err != nil {
		return err
	}
	if status == "failed" {
		return nil
	}
	if err := m.financeAudit(ctx, tx, rid, status, "failed", msg); err != nil {
		return err
	}
	var number string
	var prop uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT number, property_id FROM hris.payroll_runs WHERE id = $1`, rid).Scan(&number, &prop); err != nil {
		return err
	}
	return m.notifyUsers(ctx, tx, prop, holders(ctx, tx, prop, PermRunPost), NotifyRunPostingFailed, "/hris/payroll/runs/"+rid.String(),
		map[string]any{"number": number, "message": msg})
}

func (m *Module) financeAudit(ctx context.Context, tx pgx.Tx, rid uuid.UUID, from, to, note string) error {
	var number string
	var prop uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT number, property_id FROM hris.payroll_runs WHERE id = $1`, rid).Scan(&number, &prop); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "finance_" + to, EntityType: "hris.payroll_run", EntityID: rid.String(),
		EntityLabel: number, PropertyID: &prop, Reason: note, ActorName: "Finance & Accounting", Before: map[string]any{"financeStatus": from},
		After: map[string]any{"financeStatus": to}})
}
