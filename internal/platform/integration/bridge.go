package integration

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/audit"
)

// BridgeAgent is a local bridge agent installed in the club network for
// hardware (locker, turnstile, ball dispenser). Hardware is never exposed to
// the internet; the agent connects outbound (FR-INT-07, Technical Doc §8.2).
type BridgeAgent struct {
	ID              uuid.UUID      `json:"id"`
	PropertyID      uuid.UUID      `json:"propertyId"`
	Name            string         `json:"name"`
	Status          string         `json:"status" enum:"active,inactive"`
	AgentVersion    *string        `json:"agentVersion"`
	Hardware        []any          `json:"hardware"`
	LastHeartbeatAt *time.Time     `json:"lastHeartbeatAt"`
	Online          bool           `json:"online" doc:"Heartbeat received within the last 2 minutes"`
	LastIP          *string        `json:"lastIp"`
	CreatedAt       time.Time      `json:"createdAt"`
	Token           string         `json:"token,omitempty" doc:"Agent token, returned only at registration"`
	Meta            map[string]any `json:"meta,omitempty"`
}

type BridgeAgentRequest struct {
	Name string `json:"name"`
}

type BridgeAgentUpdate struct {
	Name   *string `json:"name,omitempty"`
	Status *string `json:"status,omitempty" enum:"active,inactive"`
}

type HeartbeatRequest struct {
	AgentVersion string           `json:"agentVersion"`
	Hardware     []map[string]any `json:"hardware"`
}

type HeartbeatResponse struct {
	AgentID         uuid.UUID `json:"agentId"`
	ServerTime      time.Time `json:"serverTime"`
	NextHeartbeatIn int       `json:"nextHeartbeatInSeconds"`
	Commands        []any     `json:"commands"`
}

const agentSelect = `SELECT id, property_id, name, status, agent_version, hardware, last_heartbeat_at, last_ip, created_at FROM platform.bridge_agents`

func scanAgent(row pgx.Row) (BridgeAgent, error) {
	var a BridgeAgent
	err := row.Scan(&a.ID, &a.PropertyID, &a.Name, &a.Status, &a.AgentVersion, &a.Hardware, &a.LastHeartbeatAt, &a.LastIP, &a.CreatedAt)
	if a.Hardware == nil {
		a.Hardware = []any{}
	}
	a.Online = a.LastHeartbeatAt != nil && time.Since(*a.LastHeartbeatAt) < 2*time.Minute
	return a, err
}

func (h *HTTP) listAgents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []BridgeAgent{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, agentSelect+` ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAgent(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[BridgeAgent]{Items: out})
}

func (h *HTTP) createAgent(w http.ResponseWriter, r *http.Request) {
	var req BridgeAgentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpx.WriteError(w, r, errs.Validation("invalid_agent", "name is required", errs.Field("name", "required", "name is required")))
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	token := "ocb_" + secret.RandomToken(32)
	var out BridgeAgent
	err := h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		aid := id.New()
		uid := id.Ptr(authz.From(ctx).UserID)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.bridge_agents (id, property_id, name, token_hash, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $5)`, aid, pid, req.Name, secret.HashToken(token), uid); err != nil {
			return err
		}
		var err error
		out, err = scanAgent(tx.QueryRow(ctx, agentSelect+` WHERE id = $1`, aid))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, Category: audit.CategorySecurity,
			EntityType: "platform.bridge_agent", EntityID: aid.String(), EntityLabel: req.Name, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out.Token = token
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *HTTP) updateAgent(w http.ResponseWriter, r *http.Request) {
	aid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req BridgeAgentUpdate
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.Status != nil && *req.Status != "active" && *req.Status != "inactive" {
		httpx.WriteError(w, r, errs.Validation("invalid_status", "invalid status", errs.Field("status", "invalid", "active or inactive")))
		return
	}
	ctx := r.Context()
	var out BridgeAgent
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := scanAgent(tx.QueryRow(ctx, agentSelect+` WHERE id = $1 FOR UPDATE`, aid))
		if dbtx.IsNoRows(err) {
			return errs.NotFound("bridge agent")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.bridge_agents SET name = coalesce($2, name), status = coalesce($3, status), updated_by = $4 WHERE id = $1`,
			aid, req.Name, req.Status, id.Ptr(authz.From(ctx).UserID)); err != nil {
			return err
		}
		out, err = scanAgent(tx.QueryRow(ctx, agentSelect+` WHERE id = $1`, aid))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionUpdate, EntityType: "platform.bridge_agent",
			EntityID: aid.String(), EntityLabel: out.Name, Before: before, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// heartbeat is called by the agent every 30 s with its bearer token.
func (h *HTTP) heartbeat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if !strings.HasPrefix(token, "ocb_") {
		httpx.WriteError(w, r, errs.Unauthorized("agent token required"))
		return
	}
	var req HeartbeatRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	aid, _, err := h.Svc.AgentByToken(ctx, token)
	if dbtx.IsNoRows(err) {
		httpx.WriteError(w, r, errs.Unauthorized("unknown or inactive agent"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	hw := req.Hardware
	if hw == nil {
		hw = []map[string]any{}
	}
	cmds := []any{}
	err = h.Svc.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.bridge_agents SET last_heartbeat_at = now(), agent_version = $2, hardware = $3, last_ip = $4 WHERE id = $1`,
			aid, req.AgentVersion, hw, reqctx.GetMeta(ctx).IP); err != nil {
			return err
		}
		queued, err := pending(ctx, tx, aid)
		for _, c := range queued {
			cmds = append(cmds, c)
		}
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, HeartbeatResponse{AgentID: aid, ServerTime: time.Now().UTC(), NextHeartbeatIn: 30, Commands: cmds})
}

func (h *HTTP) registerBridge(reg *route.Registry) {
	const tag = "Integrations"
	add := func(rt route.Route) { rt.Module = "platform"; rt.Tag = tag; reg.Add(rt) }
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/bridge-agents", Summary: "List bridge agents",
		Permission: "platform.bridge_agent.view", Scope: route.ScopeProperty, Response: BridgeAgent{}, List: true, Handler: h.listAgents})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/bridge-agents", Summary: "Register bridge agent",
		Permission: "platform.bridge_agent.manage", Scope: route.ScopeProperty, Request: BridgeAgentRequest{}, Response: BridgeAgent{}, Handler: h.createAgent})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/bridge-agents/{id}", Summary: "Edit bridge agent",
		Permission: "platform.bridge_agent.manage", Scope: route.ScopeProperty, Request: BridgeAgentUpdate{}, Response: BridgeAgent{}, Handler: h.updateAgent})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/bridge/heartbeat", Summary: "Bridge agent heartbeat (agent bearer token)",
		Auth: route.AuthSignature, Request: HeartbeatRequest{}, Response: HeartbeatResponse{}, Status: http.StatusOK,
		NoAudit: "high-frequency liveness signal; stored in bridge_agents.last_heartbeat_at", Handler: h.heartbeat})
}
