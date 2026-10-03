// Package realtime pushes change notifications to clients over Server-Sent
// Events (Technical Doc §3.4). Modules call Publish inside their business
// transaction; PostgreSQL delivers the NOTIFY only when the transaction
// commits, to every API replica listening on the channel, which fans it out
// to its SSE subscribers. Payloads carry ids and the event type only —
// clients refetch the data through REST.
package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/reqctx"
)

// Channel is the PostgreSQL NOTIFY channel.
const Channel = "oneclub_realtime"

// Event is one notification.
type Event struct {
	Topic      string         `json:"topic"`                // e.g. golf.tee_sheet, golf.starter_queue
	Type       string         `json:"type"`                 // e.g. booking_confirmed
	PropertyID *uuid.UUID     `json:"propertyId,omitempty"` // subscribers only see their property
	Data       map[string]any `json:"data,omitempty"`       // ids (courseId, date, bookingId …)
	At         time.Time      `json:"at"`
}

// Publish queues a notification that is delivered when tx commits.
func Publish(ctx context.Context, tx pgx.Tx, topic, typ string, property *uuid.UUID, data map[string]any) error {
	raw, err := json.Marshal(Event{Topic: topic, Type: typ, PropertyID: property, Data: data, At: clock.Now()})
	if err != nil {
		return err
	}
	if len(raw) > 7900 {
		return fmt.Errorf("realtime: payload too large (%d bytes)", len(raw))
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, Channel, string(raw))
	return err
}

type subscriber struct {
	topics   []string
	property *uuid.UUID
	filter   map[string]string
	ch       chan Event
}

func (s *subscriber) wants(e Event) bool {
	if s.property != nil && e.PropertyID != nil && *s.property != *e.PropertyID {
		return false
	}
	match := false
	for _, t := range s.topics {
		if t == e.Topic || strings.HasPrefix(e.Topic, t+".") {
			match = true
		}
	}
	if !match {
		return false
	}
	for k, v := range s.filter {
		if got, ok := e.Data[k]; ok && fmt.Sprint(got) != v {
			return false
		}
	}
	return true
}

// Hub listens on the channel and fans events out to local subscribers.
type Hub struct {
	Pool *pgxpool.Pool

	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.RWMutex
	subs   map[*subscriber]struct{}
	// Delivered counts events received from PostgreSQL (diagnostics/tests).
	Delivered atomic.Int64
}

// Start begins listening (idempotent). The listener reconnects on errors.
func (h *Hub) Start() {
	h.once.Do(func() {
		h.mu.Lock()
		if h.subs == nil {
			h.subs = map[*subscriber]struct{}{}
		}
		h.mu.Unlock()
		if h.Pool == nil {
			return
		}
		h.ctx, h.cancel = context.WithCancel(context.Background())
		ready := make(chan struct{})
		go h.listen(ready)
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
		}
	})
}

// Stop ends the listener and releases its connection (graceful shutdown).
func (h *Hub) Stop() {
	h.once.Do(func() {}) // a hub never started cannot start any more
	if h.cancel != nil {
		h.cancel()
	}
}

func (h *Hub) listen(ready chan struct{}) {
	signalled := false
	for {
		err := h.listenOnce(func() {
			if !signalled {
				signalled = true
				close(ready)
			}
		})
		if h.ctx.Err() != nil {
			return
		}
		slog.Warn("realtime listener stopped; reconnecting", "err", err)
		time.Sleep(time.Second)
	}
}

func (h *Hub) listenOnce(onReady func()) error {
	ctx := h.ctx
	conn, err := h.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return err
	}
	onReady()
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			conn.Conn().Close(context.Background()) //nolint:errcheck // broken connection is discarded
			return err
		}
		var e Event
		if json.Unmarshal([]byte(n.Payload), &e) != nil {
			continue
		}
		h.dispatch(e)
	}
}

func (h *Hub) dispatch(e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	h.Delivered.Add(1)
	for s := range h.subs {
		if !s.wants(e) {
			continue
		}
		select {
		case s.ch <- e:
		default: // slow client: drop; it refetches on the next event
		}
	}
}

func (h *Hub) subscribe(topics []string, property *uuid.UUID, filter map[string]string) *subscriber {
	h.Start()
	s := &subscriber{topics: topics, property: property, filter: filter, ch: make(chan Event, 64)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

func (h *Hub) unsubscribe(s *subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// Subscribers returns the number of open streams (diagnostics).
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// Heartbeat is the keep-alive interval of SSE streams.
var Heartbeat = 15 * time.Second

// Stream returns an SSE handler for topics. Query parameters listed in
// filterParams (e.g. courseId, date) narrow the events delivered.
func (h *Hub) Stream(topics []string, filterParams ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Time{}) // streams outlive the server WriteTimeout
		var prop *uuid.UUID
		if pid, ok := reqctx.Property(r.Context()); ok {
			prop = &pid
		}
		filter := map[string]string{}
		for _, p := range filterParams {
			if v := r.URL.Query().Get(p); v != "" {
				filter[p] = v
			}
		}
		s := h.subscribe(topics, prop, filter)
		defer h.unsubscribe(s)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "retry: 3000\nevent: ready\ndata: {\"at\":%q}\n\n", clock.Now().Format(time.RFC3339Nano))
		_ = rc.Flush()
		tick := time.NewTicker(Heartbeat)
		defer tick.Stop()
		var stopped <-chan struct{} // nil (never) unless the listener runs
		if h.ctx != nil {
			stopped = h.ctx.Done()
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-stopped: // server shutdown: clients reconnect elsewhere
				return
			case <-tick.C:
				if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
					return
				}
				_ = rc.Flush()
			case e := <-s.ch:
				raw, _ := json.Marshal(e)
				if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Topic, raw); err != nil {
					return
				}
				_ = rc.Flush()
			}
		}
	}
}
