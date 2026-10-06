package accounting

// FR-PST-04 Posting Exception queue: every consumed event that could not
// be posted completely (no matching rule, missing or inactive account,
// closed period, invalid payload) is kept here with its stored event, so
// nothing is lost. An exception is resolved by a repost (after the rule or
// account is fixed: the suspense journal is reversed and the event is
// processed again) or ignored with a reason.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// AccountingPostingException is one entry of the posting exception queue.
type AccountingPostingException struct {
	ID                   uuid.UUID       `json:"id" db:"id"`
	PropertyID           uuid.UUID       `json:"propertyId" db:"property_id"`
	EventID              *uuid.UUID      `json:"eventId" db:"event_id"`
	EventType            string          `json:"eventType" db:"event_type"`
	SourceType           *string         `json:"sourceType" db:"source_type"`
	SourceID             *string         `json:"sourceId" db:"source_id"`
	Reason               string          `json:"reason" db:"reason" enum:"missing_rule,missing_account,closed_period,invalid_payload,no_book,unbalanced"`
	Message              string          `json:"message" db:"message"`
	Details              json.RawMessage `json:"details" db:"details"`
	Amount               string          `json:"amount" db:"amount"`
	JournalID            *uuid.UUID      `json:"journalId" db:"journal_id" doc:"Suspense journal posted for the event (suspense mode)"`
	JournalNumber        *string         `json:"journalNumber" db:"journal_number"`
	Status               string          `json:"status" db:"status" enum:"open,resolved,ignored"`
	Attempts             int             `json:"attempts" db:"attempts"`
	ResolvedAt           *time.Time      `json:"resolvedAt" db:"resolved_at"`
	ResolvedBy           *uuid.UUID      `json:"resolvedBy" db:"resolved_by"`
	ResolutionNote       *string         `json:"resolutionNote" db:"resolution_note"`
	ResolutionJournalIDs []uuid.UUID     `json:"resolutionJournalIds" db:"resolution_journal_ids"`
	CreatedAt            time.Time       `json:"createdAt" db:"created_at"`
	EventPayload         json.RawMessage `json:"eventPayload,omitempty" db:"event_payload"`
}

const exceptionSelect = `SELECT e.id, e.property_id, e.event_id, e.event_type, e.source_type, e.source_id, e.reason, e.message, e.details,
	trim_scale(e.amount)::text AS amount, e.journal_id, (SELECT j.number FROM accounting.journals j WHERE j.id = e.journal_id) AS journal_number,
	e.status, e.attempts, e.resolved_at, e.resolved_by, e.resolution_note, e.resolution_journal_ids, e.created_at,
	(SELECT p.payload FROM accounting.processed_events p WHERE p.event_id = e.event_id) AS event_payload
	FROM accounting.posting_exceptions e`

func getException(ctx context.Context, q dbtx.Querier, exID uuid.UUID) (AccountingPostingException, error) {
	return getOne[AccountingPostingException]("posting exception")(q.Query(ctx, exceptionSelect+` WHERE e.id = $1`, exID))
}

// ListExceptions lists the posting exceptions of a property.
func ListExceptions(ctx context.Context, q dbtx.Querier, property uuid.UUID, status, eventType, reason string, limit int) ([]AccountingPostingException, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return handle.List[AccountingPostingException](q.Query(ctx, exceptionSelect+` WHERE e.property_id = $1 AND ($2 = '' OR e.status = $2)
		AND ($3 = '' OR e.event_type = $3) AND ($4 = '' OR e.reason = $4) ORDER BY e.created_at DESC LIMIT $5`, property, status, eventType, reason, limit))
}

// ExceptionResolveInput carries the reason of an exception decision.
type ExceptionResolveInput struct {
	Reason string `json:"reason,omitempty"`
}

// RepostException reposts an exception with the rules and accounts in
// force (FR-PST-04).
func (m *Module) RepostException(ctx context.Context, tx pgx.Tx, property, exID uuid.UUID) (AccountingPostingException, error) {
	before, err := getException(ctx, tx, exID)
	if err != nil {
		return before, err
	}
	if before.PropertyID != property {
		return before, errs.NotFound("posting exception")
	}
	x, err := m.repostException(ctx, tx, exID)
	if err != nil {
		return x, err
	}
	x.EventPayload = nil
	return x, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "repost", EntityType: "accounting.posting_exception", EntityID: exID.String(),
		EntityLabel: before.EventType + " · " + before.Reason, PropertyID: &property, Before: map[string]any{"status": before.Status},
		After: map[string]any{"status": x.Status, "resolutionJournalIds": x.ResolutionJournalIDs, "note": x.ResolutionNote}})
}

// IgnoreException closes an exception without posting (e.g. a test event);
// a suspense journal it posted stays until reversed manually.
func (m *Module) IgnoreException(ctx context.Context, tx pgx.Tx, property, exID uuid.UUID, in ExceptionResolveInput) (AccountingPostingException, error) {
	x, err := getException(ctx, tx, exID)
	if err != nil {
		return x, err
	}
	if x.PropertyID != property {
		return x, errs.NotFound("posting exception")
	}
	if x.Status != "open" {
		return x, errs.Conflict("exception_closed", "the exception is already "+x.Status)
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return x, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.posting_exceptions SET status = 'ignored', resolved_at = now(), resolved_by = $2, resolution_note = $3
		WHERE id = $1`, exID, actorPtr(ctx), in.Reason); err != nil {
		return x, err
	}
	after, err := getException(ctx, tx, exID)
	if err != nil {
		return after, err
	}
	after.EventPayload = nil
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "ignore", EntityType: "accounting.posting_exception", EntityID: exID.String(),
		EntityLabel: x.EventType + " · " + x.Reason, PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": x.Status},
		After: map[string]any{"status": after.Status}})
}

// AccountingProcessedEvent is a consumed event and what it posted (FR-PST-05 trace).
type AccountingProcessedEvent struct {
	EventID     uuid.UUID   `json:"eventId" db:"event_id"`
	EventType   string      `json:"eventType" db:"event_type"`
	OccurredAt  *time.Time  `json:"occurredAt" db:"occurred_at"`
	Status      string      `json:"status" db:"status" enum:"posted,no_posting,exception,skipped"`
	JournalIDs  []uuid.UUID `json:"journalIds" db:"journal_ids"`
	Note        *string     `json:"note" db:"note"`
	Attempts    int         `json:"attempts" db:"attempts"`
	ProcessedAt time.Time   `json:"processedAt" db:"processed_at"`
}

// ListProcessedEvents lists the consumed events of a property.
func ListProcessedEvents(ctx context.Context, q dbtx.Querier, property uuid.UUID, eventType, status string, limit int) ([]AccountingProcessedEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return handle.List[AccountingProcessedEvent](q.Query(ctx, `SELECT event_id, event_type, occurred_at, status, journal_ids, note, attempts, processed_at
		FROM accounting.processed_events WHERE property_id = $1 AND ($2 = '' OR event_type = $2) AND ($3 = '' OR status = $3)
		ORDER BY processed_at DESC LIMIT $4`, property, eventType, status, limit))
}
