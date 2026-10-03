// Package sync receives the offline queue of operational apps (Technical
// Doc §6.4, PRD FR-SH-05): items are sent in order, processed idempotently
// by their client UUIDv7, and each gets a result or a conflict back. P1
// modules (POS, starter, caddy) register their own actions.
package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
)

// Handler processes one action inside the item's transaction. Returning a
// *ConflictError marks the item as a conflict the client must resolve.
type Handler func(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error)

// ConflictError is an expected business conflict (e.g. assignment changed).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// Service holds registered actions.
type Service struct {
	DB *dbtx.DB
	mu sync.RWMutex
	h  map[string]Handler
}

func New(db *dbtx.DB) *Service {
	s := &Service{DB: db, h: map[string]Handler{}}
	s.Handle("ops.shift_note", shiftNote)
	return s
}

// Handle registers an action handler.
func (s *Service) Handle(action string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.h[action] = h
}

// Item is one queued offline action.
type Item struct {
	ID         uuid.UUID       `json:"id" doc:"Client-generated UUIDv7 (idempotency key)"`
	Action     string          `json:"action"`
	Payload    json.RawMessage `json:"payload"`
	ClientTime *time.Time      `json:"clientTime,omitempty"`
}

type Request struct {
	Items []Item `json:"items"`
}

type ItemResult struct {
	ID     uuid.UUID       `json:"id"`
	Status string          `json:"status" enum:"accepted,duplicate,rejected,conflict"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type Response struct {
	Results []ItemResult `json:"results"`
}

// shiftNote is the P0 reference action: a note written during the shift
// (used by the offline acceptance test of the Ops shell).
func shiftNote(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || strings.TrimSpace(p.Text) == "" {
		return nil, errs.Validation("invalid_note", "note text is required")
	}
	if len(p.Text) > 2000 {
		return nil, errs.Validation("invalid_note", "note is too long")
	}
	return map[string]any{"text": p.Text, "recordedAt": time.Now().UTC()}, nil
}

func (s *Service) handle(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(req.Items) > 200 {
		httpx.WriteError(w, r, errs.Validation("too_many_items", "send at most 200 items per request"))
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	var prop *uuid.UUID
	if pid, ok := reqctx.Property(ctx); ok {
		prop = &pid
	}
	out := Response{Results: []ItemResult{}}
	for _, it := range req.Items {
		res := ItemResult{ID: it.ID}
		err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var status string
			var stored []byte
			err := tx.QueryRow(ctx, `SELECT status, result FROM platform.sync_items WHERE id = $1 AND user_id = $2`, it.ID, p.UserID).Scan(&status, &stored)
			if err == nil {
				res.Status, res.Result = "duplicate", stored
				return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "sync_duplicate", EntityType: "platform.sync_item",
					EntityID: it.ID.String(), EntityLabel: it.Action, PropertyID: prop})
			}
			if !dbtx.IsNoRows(err) {
				return err
			}
			s.mu.RLock()
			h, ok := s.h[it.Action]
			s.mu.RUnlock()
			status = "accepted"
			var result any
			if !ok {
				status, res.Error = "rejected", "unknown action "+it.Action
			} else {
				sp, err := tx.Begin(ctx)
				if err != nil {
					return err
				}
				result, err = h(ctx, sp, it.Payload)
				if err != nil {
					_ = sp.Rollback(ctx)
					if ce, ok := err.(*ConflictError); ok {
						status, res.Error = "conflict", ce.Message
					} else if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
						status, res.Error = "rejected", de.Message
					} else {
						return err
					}
				} else if err := sp.Commit(ctx); err != nil {
					return err
				}
			}
			raw, _ := json.Marshal(result)
			if result == nil {
				raw = []byte("{}")
			}
			payload := it.Payload
			if len(payload) == 0 {
				payload = json.RawMessage("{}")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO platform.sync_items (id, user_id, device_id, property_id, action, payload, status, result, client_time)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, it.ID, p.UserID, p.DeviceID, prop, it.Action, []byte(payload), status, raw, it.ClientTime); err != nil {
				return err
			}
			res.Status, res.Result = status, raw
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "sync_" + status, EntityType: "platform.sync_item",
				EntityID: it.ID.String(), EntityLabel: it.Action, PropertyID: prop, After: map[string]any{"action": it.Action, "payload": payload},
				Metadata: map[string]any{"clientTime": it.ClientTime}})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out.Results = append(out.Results, res)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// Register adds POST /api/v1/platform/sync.
func (s *Service) Register(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/sync", Module: "platform", Tag: "Offline Sync",
		Summary:     "Submit the offline queue (ordered; idempotent per item id)",
		Description: "Each item is processed once; resubmitted ids return status duplicate with the stored result.",
		Permission:  "platform.ops.access", Request: Request{}, Response: Response{}, Status: http.StatusOK, Handler: s.handle})
}
