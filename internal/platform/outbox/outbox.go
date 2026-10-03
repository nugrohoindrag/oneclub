// Package outbox implements the transactional outbox and in-process domain
// events (FR-JOB-02, Technical Doc §4.3):
//
//	Module (in a DB transaction): change data + outbox.Publish  ← commit together
//	River job "outbox_dispatch":   call registered subscribers in-process
//
// Delivery is at-least-once; each subscriber runs inside the dispatch
// transaction together with its (subscriber, event_id) dedupe marker, so
// its database effects happen exactly once even across worker restarts.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/jobs"
)

// Event is a domain event.
type Event struct {
	ID            uuid.UUID       `json:"id"`
	Type          string          `json:"type"` // <module>.<event>, e.g. platform.venue_activated
	AggregateType string          `json:"aggregateType"`
	AggregateID   *uuid.UUID      `json:"aggregateId"`
	PropertyID    *uuid.UUID      `json:"propertyId"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    time.Time       `json:"occurredAt"`
}

// Decode unmarshals the payload.
func (e Event) Decode(v any) error { return json.Unmarshal(e.Payload, v) }

// Handler processes one event inside the dispatch transaction.
type Handler func(ctx context.Context, tx pgx.Tx, e Event) error

type subscriber struct {
	name string
	fn   Handler
}

// Bus holds subscribers and publishes events.
type Bus struct {
	Jobs *jobs.Client
	mu   sync.RWMutex
	subs map[string][]subscriber
}

func NewBus() *Bus { return &Bus{subs: map[string][]subscriber{}} }

// Subscribe registers a named, idempotent subscriber for an event type.
func (b *Bus) Subscribe(eventType, name string, fn Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[eventType] = append(b.subs[eventType], subscriber{name: name, fn: fn})
}

func (b *Bus) subscribers(eventType string) []subscriber {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]subscriber(nil), b.subs[eventType]...)
}

// EventTypes lists event types with subscribers (diagnostics).
func (b *Bus) EventTypes() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []string
	for k := range b.subs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Publish writes an event to the outbox within tx and schedules a dispatch.
func (b *Bus) Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, err
	}
	eid := id.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.outbox (id, event_type, aggregate_type, aggregate_id, property_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6)`, eid, eventType, aggregateType, aggregateID, propertyID, raw); err != nil {
		return uuid.Nil, fmt.Errorf("outbox publish: %w", err)
	}
	if b.Jobs != nil {
		if _, err := b.Jobs.Insert(ctx, tx, DispatchArgs{}, &river.InsertOpts{
			Queue: jobs.QueueOutbox,
		}); err != nil {
			return uuid.Nil, err
		}
	}
	return eid, nil
}
