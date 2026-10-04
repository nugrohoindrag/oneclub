package integration

// Bridge hardware commands (PRD P2 FR-INT-P2-01): modules queue a command
// for a device (ball dispenser, turnstile, locker, thermal printer, cash
// drawer); the local bridge agent receives queued commands in its heartbeat
// response and reports the result. When no online agent serves the device
// the caller falls back to the manual flow (PRD P2 §6 #8).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// DeviceKinds served through the bridge agent.
var DeviceKinds = []string{"ball_dispenser", "turnstile", "locker", "printer", "cash_drawer", "golf_cart_gps"}

// Command is a queued hardware command.
type Command struct {
	ID          uuid.UUID      `json:"id" db:"id"`
	AgentID     uuid.UUID      `json:"agentId" db:"agent_id"`
	Device      string         `json:"device" db:"device"`
	Command     string         `json:"command" db:"command"`
	Payload     map[string]any `json:"payload" db:"payload"`
	Status      string         `json:"status" db:"status" enum:"queued,sent,succeeded,failed,expired"`
	Result      map[string]any `json:"result" db:"result"`
	SourceType  *string        `json:"sourceType" db:"source_type"`
	SourceID    *uuid.UUID     `json:"sourceId" db:"source_id"`
	Deadline    time.Time      `json:"deadline" db:"deadline"`
	CreatedAt   time.Time      `json:"createdAt" db:"created_at"`
	CompletedAt *time.Time     `json:"completedAt" db:"completed_at"`
}

const commandSelect = `SELECT id, agent_id, device, command, payload, status, result, source_type, source_id, deadline, created_at, completed_at FROM platform.bridge_commands`

// CommandRequest queues a command.
type CommandRequest struct {
	PropertyID uuid.UUID
	Device     string // device name or kind as reported by the agent hardware list
	Command    string
	Payload    map[string]any
	SourceType string
	SourceID   *uuid.UUID
	TTL        time.Duration
}

// Enqueue queues a command for the online agent serving the device; nil
// when no agent serves it (the caller uses the manual fallback).
func Enqueue(ctx context.Context, tx pgx.Tx, r CommandRequest) (*Command, error) {
	var aid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT a.id FROM platform.bridge_agents a WHERE a.property_id = $1 AND a.status = 'active'
		AND a.last_heartbeat_at > now() - interval '2 minutes'
		AND EXISTS (SELECT 1 FROM jsonb_array_elements(a.hardware) h WHERE h->>'device' = $2 OR h->>'kind' = $2)
		ORDER BY a.last_heartbeat_at DESC LIMIT 1`, r.PropertyID, r.Device).Scan(&aid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if r.TTL == 0 {
		r.TTL = 2 * time.Minute
	}
	if r.Payload == nil {
		r.Payload = map[string]any{}
	}
	raw, _ := json.Marshal(r.Payload)
	cid := id.New()
	var src *string
	if r.SourceType != "" {
		src = &r.SourceType
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.bridge_commands (id, property_id, agent_id, device, command, payload, source_type, source_id, deadline, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, cid, r.PropertyID, aid, r.Device, r.Command, raw, src, r.SourceID, time.Now().Add(r.TTL), actorID(ctx)); err != nil {
		return nil, err
	}
	c, err := handle.Get[Command](tx.Query(ctx, commandSelect+` WHERE id = $1`, cid))
	return &c, err
}

// pending returns queued commands for an agent and marks them sent.
func pending(ctx context.Context, tx pgx.Tx, aid uuid.UUID) ([]Command, error) {
	if _, err := tx.Exec(ctx, `UPDATE platform.bridge_commands SET status = 'expired', completed_at = now()
		WHERE agent_id = $1 AND status IN ('queued', 'sent') AND deadline < now()`, aid); err != nil {
		return nil, err
	}
	cmds, err := handle.List[Command](tx.Query(ctx, commandSelect+` WHERE agent_id = $1 AND status = 'queued' ORDER BY created_at LIMIT 50 FOR UPDATE`, aid))
	if err != nil || len(cmds) == 0 {
		return cmds, err
	}
	ids := make([]uuid.UUID, len(cmds))
	for i, c := range cmds {
		ids[i] = c.ID
	}
	_, err = tx.Exec(ctx, `UPDATE platform.bridge_commands SET status = 'sent', sent_at = now() WHERE id = ANY($1)`, ids)
	return cmds, err
}

type CommandResult struct {
	Success bool           `json:"success"`
	Result  map[string]any `json:"result,omitempty"`
}

func actorID(ctx context.Context) *uuid.UUID {
	if u := handle.UserID(ctx); u != uuid.Nil {
		return &u
	}
	return nil
}

func (h *HTTP) commandResult(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	aid, pid, err := h.Svc.AgentByToken(ctx, token)
	if err != nil || !strings.HasPrefix(token, "ocb_") {
		httpx.WriteError(w, r, errs.Unauthorized("unknown or inactive agent"))
		return
	}
	cid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var in CommandResult
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := "failed"
	if in.Success {
		status = "succeeded"
	}
	if in.Result == nil {
		in.Result = map[string]any{}
	}
	var out Command
	err = h.Svc.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		raw, _ := json.Marshal(in.Result)
		tag, err := tx.Exec(ctx, `UPDATE platform.bridge_commands SET status = $3, result = $4, completed_at = now()
			WHERE id = $1 AND agent_id = $2 AND status IN ('queued', 'sent')`, cid, aid, status, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errs.Conflict("not_pending", "command is not pending for this agent")
		}
		if out, err = handle.Get[Command](tx.Query(ctx, commandSelect+` WHERE id = $1`, cid)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "bridge_result", EntityType: "platform.bridge_command",
			EntityID: cid.String(), EntityLabel: out.Device + " " + out.Command, PropertyID: &pid, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *HTTP) registerBridgeCommands(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/bridge/commands/{id}:result", Summary: "Bridge agent reports a command result (agent bearer token)",
		Module: "platform", Tag: "Integrations", Auth: route.AuthSignature, Request: CommandResult{}, Response: Command{}, Status: http.StatusOK,
		Handler: h.commandResult})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/bridge-commands", Summary: "Bridge hardware commands", Module: "platform",
		Tag: "Integrations", Permission: "platform.bridge_agent.view", Scope: route.ScopeProperty, Response: Command{}, List: true,
		Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[device]"}},
		Handler: handle.Read(h.Svc.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Command], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Command](tx.Query(ctx, commandSelect+` WHERE property_id = $1 AND ($2 = '' OR status = $2)
				AND ($3 = '' OR device = $3) ORDER BY created_at DESC LIMIT $4`, handle.Property(ctx), lp.Filters["status"], lp.Filters["device"], lp.Limit)))
		})})
}
