// Package audit records every data change and security event (EP-07) into
// the append-only, monthly-partitioned audit.audit_log table. It is the
// public interface other modules use: audit.Record(ctx, tx, entry) inside
// the same transaction as the change, so data and audit commit together.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/reqctx"
)

// Standard actions (FR-AUD-01, FR-AUD-02).
const (
	ActionCreate       = "create"
	ActionUpdate       = "update"
	ActionStatusChange = "status_change"
	ActionDelete       = "delete"
	ActionArchive      = "archive"
	ActionVoid         = "void"
	ActionExport       = "export"
	ActionImport       = "import"

	ActionLoginSucceeded   = "login_succeeded"
	ActionLoginFailed      = "login_failed"
	ActionLogout           = "logout"
	ActionMFAEnrolled      = "mfa_enrolled"
	ActionMFAVerified      = "mfa_verified"
	ActionMFAFailed        = "mfa_failed"
	ActionMFAReset         = "mfa_reset"
	ActionPasswordReset    = "password_reset"
	ActionPasswordChanged  = "password_changed"
	ActionSessionRevoked   = "session_revoked"
	ActionAccountLocked    = "account_locked"
	ActionRoleAssigned     = "role_assigned"
	ActionRoleUnassigned   = "role_unassigned"
	ActionPermissionChange = "permissions_changed"
)

// Categories.
const (
	CategoryData     = "data"
	CategorySecurity = "security"
	CategorySystem   = "system"
)

// Entry describes one audited event.
type Entry struct {
	Module      string
	Action      string
	Category    string // default data
	EntityType  string // e.g. "platform.venue"
	EntityID    string
	EntityLabel string
	PropertyID  *uuid.UUID
	Before      any
	After       any
	Reason      string
	Metadata    map[string]any
	// ActorName overrides the actor for anonymous security events (e.g.
	// failed login with an unknown e-mail).
	ActorName string
}

// Record writes e using q (normally the business transaction).
func Record(ctx context.Context, q dbtx.Querier, e Entry) error {
	before, err := snapshot(e.Before)
	if err != nil {
		return err
	}
	after, err := snapshot(e.After)
	if err != nil {
		return err
	}
	if e.Category == "" {
		e.Category = CategoryData
	}
	meta := reqctx.GetMeta(ctx)
	p := authz.From(ctx)
	actorType := "anonymous"
	var actorID *uuid.UUID
	actorName := e.ActorName
	var roles []string
	var deviceID, sessionID *uuid.UUID
	if p != nil {
		actorType = string(p.Kind)
		switch p.Kind {
		case authz.ActorUser, authz.ActorDevice:
			actorID = id.Ptr(p.UserID)
		case authz.ActorAPIKey:
			actorID = id.Ptr(p.APIKeyID)
		}
		if actorName == "" {
			actorName = p.Name
		}
		roles = p.RoleCodes()
		deviceID = p.DeviceID
		sessionID = id.Ptr(p.SessionID)
	}
	if roles == nil {
		roles = []string{}
	}
	if e.PropertyID == nil {
		if pid, ok := reqctx.Property(ctx); ok {
			e.PropertyID = &pid
		}
	}
	md := e.Metadata
	if md == nil {
		md = map[string]any{}
	}
	if meta.RouteID != "" {
		md["route"] = meta.RouteID
	}
	mdJSON, _ := json.Marshal(md)
	_, err = q.Exec(ctx, `
		INSERT INTO audit.audit_log (id, actor_type, actor_id, actor_name, actor_roles, property_id, module, action, category,
		  entity_type, entity_id, entity_label, before, after, reason, ip, user_agent, device_id, session_id, request_id, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		id.New(), actorType, actorID, nullStr(actorName), roles, e.PropertyID, e.Module, e.Action, e.Category,
		e.EntityType, nullStr(e.EntityID), nullStr(e.EntityLabel), before, after, nullStr(e.Reason),
		nullStr(meta.IP), nullStr(truncate(meta.UserAgent, 400)), deviceID, sessionID, nullStr(meta.RequestID), mdJSON)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if t := reqctx.GetAuditTracker(ctx); t != nil {
		t.Count++
	}
	return nil
}

// snapshot converts a value to a JSON object with secrets stripped
// (password hashes, tokens and credentials never reach the audit log).
func snapshot(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("audit snapshot: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// non-object values are wrapped
		return json.Marshal(map[string]any{"value": json.RawMessage(raw)})
	}
	return json.Marshal(mask.StripSecrets(m))
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Diff returns only the keys whose values differ between before and after,
// used to keep update entries compact while preserving full before/after.
func Diff(before, after map[string]any) []string {
	var changed []string
	for k, av := range after {
		bv, ok := before[k]
		bj, _ := json.Marshal(bv)
		aj, _ := json.Marshal(av)
		if !ok || string(bj) != string(aj) {
			changed = append(changed, k)
		}
	}
	return changed
}
