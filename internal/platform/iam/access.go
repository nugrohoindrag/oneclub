package iam

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/audit"
)

// ── Sessions (FR-IAM-10) ──────────────────────────────────────────────────

type Session struct {
	ID         uuid.UUID  `json:"id"`
	UserID     uuid.UUID  `json:"userId"`
	UserName   string     `json:"userName"`
	Kind       string     `json:"kind" enum:"web,device"`
	DeviceID   *uuid.UUID `json:"deviceId"`
	IP         *string    `json:"ip"`
	UserAgent  *string    `json:"userAgent"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastSeenAt time.Time  `json:"lastSeenAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	Current    bool       `json:"current"`
}

func (s *Service) listSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authz.From(ctx)
	target := p.UserID
	if v := r.URL.Query().Get("userId"); v != "" {
		u, err := uuid.Parse(v)
		if err != nil {
			respond(w, r, 0, nil, errs.BadRequest("invalid_user", "invalid userId"))
			return
		}
		if u != p.UserID && !p.Can("platform.session.view_all", nil) {
			respond(w, r, 0, nil, errs.Forbidden("missing permission platform.session.view_all"))
			return
		}
		target = u
	}
	var out []Session
	err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT s.id, s.user_id, u.full_name, s.kind, s.device_id, s.ip, s.user_agent, s.created_at, s.last_seen_at, s.expires_at
			FROM platform.sessions s JOIN platform.users u ON u.id = s.user_id
			WHERE s.user_id = $1 AND s.revoked_at IS NULL AND s.expires_at > now() ORDER BY s.last_seen_at DESC`, target)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x Session
			if err := rows.Scan(&x.ID, &x.UserID, &x.UserName, &x.Kind, &x.DeviceID, &x.IP, &x.UserAgent, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt); err != nil {
				return err
			}
			x.Current = x.ID == p.SessionID
			out = append(out, x)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Session{}
	}
	respond(w, r, http.StatusOK, httpx.Page[Session]{Items: out}, err)
}

func (s *Service) revokeSession(w http.ResponseWriter, r *http.Request) {
	sid, err := httpx.PathUUID(r, "id")
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var uid uuid.UUID
		var name string
		err := tx.QueryRow(ctx, `SELECT s.user_id, u.full_name FROM platform.sessions s JOIN platform.users u ON u.id = s.user_id
			WHERE s.id = $1 AND s.revoked_at IS NULL FOR UPDATE OF s`, sid).Scan(&uid, &name)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("session")
		}
		if err != nil {
			return err
		}
		if uid != p.UserID && !p.Can("platform.session.revoke_all", nil) {
			return errs.Forbidden("missing permission platform.session.revoke_all")
		}
		reason := "revoked_by_user"
		if uid != p.UserID {
			reason = "revoked_by_admin"
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now(), revoked_reason = $2 WHERE id = $1`, sid, reason); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionSessionRevoked, Category: audit.CategorySecurity,
			EntityType: "platform.session", EntityID: sid.String(), EntityLabel: name, Metadata: map[string]any{"userId": uid, "reason": reason}})
	})
	respond(w, r, 0, nil, err)
}

// ── Devices (FR-IAM-09) ───────────────────────────────────────────────────

type Device struct {
	ID          uuid.UUID  `json:"id"`
	PropertyID  uuid.UUID  `json:"propertyId"`
	Name        string     `json:"name"`
	DeviceType  string     `json:"deviceType" enum:"pos,tablet,kiosk,other"`
	OutletID    *uuid.UUID `json:"outletId"`
	Status      string     `json:"status" enum:"active,inactive"`
	LastSeenAt  *time.Time `json:"lastSeenAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	DeviceToken string     `json:"deviceToken,omitempty" doc:"Enrollment token, returned only once at registration or rotation"`
}

type DeviceRequest struct {
	Name       string     `json:"name"`
	DeviceType string     `json:"deviceType" enum:"pos,tablet,kiosk,other"`
	OutletID   *uuid.UUID `json:"outletId,omitempty"`
}

type DeviceUpdateRequest struct {
	Name     *string    `json:"name,omitempty"`
	Status   *string    `json:"status,omitempty" enum:"active,inactive"`
	OutletID *uuid.UUID `json:"outletId,omitempty"`
}

const deviceSelect = `SELECT id, property_id, name, device_type, outlet_id, status, last_seen_at, created_at FROM platform.devices`

func scanDevice(row pgx.Row) (Device, error) {
	var d Device
	err := row.Scan(&d.ID, &d.PropertyID, &d.Name, &d.DeviceType, &d.OutletID, &d.Status, &d.LastSeenAt, &d.CreatedAt)
	return d, err
}

func (s *Service) listDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out []Device
	err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, deviceSelect+` ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDevice(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Device{}
	}
	respond(w, r, http.StatusOK, httpx.Page[Device]{Items: out}, err)
}

func validDeviceType(t string) bool {
	return t == "pos" || t == "tablet" || t == "kiosk" || t == "other"
}

func (s *Service) createDevice(w http.ResponseWriter, r *http.Request) {
	var req DeviceRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	if strings.TrimSpace(req.Name) == "" || !validDeviceType(req.DeviceType) {
		respond(w, r, 0, nil, errs.Validation("invalid_device", "invalid device",
			errs.Field("name", "required", "name is required"), errs.Field("deviceType", "invalid", "pos, tablet, kiosk or other")))
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	token := "ocd_" + secret.RandomToken(32)
	var out Device
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		did := id.New()
		uid := id.Ptr(authz.From(ctx).UserID)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.devices (id, property_id, name, device_type, outlet_id, token_hash, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, did, pid, req.Name, req.DeviceType, req.OutletID, secret.HashToken(token), uid); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Validation("outlet_invalid", "outlet not found", errs.Field("outletId", "invalid", "outlet not found"))
			}
			return err
		}
		var err error
		out, err = scanDevice(tx.QueryRow(ctx, deviceSelect+` WHERE id = $1`, did))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, Category: audit.CategorySecurity,
			EntityType: "platform.device", EntityID: did.String(), EntityLabel: out.Name, After: out})
	})
	out.DeviceToken = token
	respond(w, r, http.StatusCreated, out, err)
}

func (s *Service) updateDevice(w http.ResponseWriter, r *http.Request) {
	did, err := httpx.PathUUID(r, "id")
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var req DeviceUpdateRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	if req.Status != nil && *req.Status != "active" && *req.Status != "inactive" {
		respond(w, r, 0, nil, errs.Validation("invalid_status", "invalid status", errs.Field("status", "invalid", "must be active or inactive")))
		return
	}
	ctx := r.Context()
	var out Device
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := scanDevice(tx.QueryRow(ctx, deviceSelect+` WHERE id = $1 FOR UPDATE`, did))
		if dbtx.IsNoRows(err) {
			return errs.NotFound("device")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.devices SET name = coalesce($2, name), status = coalesce($3, status),
			outlet_id = coalesce($4, outlet_id), updated_by = $5 WHERE id = $1`, did, req.Name, req.Status, req.OutletID, id.Ptr(authz.From(ctx).UserID)); err != nil {
			return err
		}
		if req.Status != nil && *req.Status == "inactive" {
			if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now(), revoked_reason = 'device_deactivated' WHERE device_id = $1 AND revoked_at IS NULL`, did); err != nil {
				return err
			}
		}
		out, err = scanDevice(tx.QueryRow(ctx, deviceSelect+` WHERE id = $1`, did))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionUpdate, Category: audit.CategorySecurity,
			EntityType: "platform.device", EntityID: did.String(), EntityLabel: out.Name, Before: before, After: out})
	})
	respond(w, r, http.StatusOK, out, err)
}

func (s *Service) rotateDeviceToken(w http.ResponseWriter, r *http.Request) {
	did, err := httpx.PathUUID(r, "id")
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	token := "ocd_" + secret.RandomToken(32)
	var out Device
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.devices SET token_hash = $2 WHERE id = $1`, did, secret.HashToken(token))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errs.NotFound("device")
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now(), revoked_reason = 'device_token_rotated' WHERE device_id = $1 AND revoked_at IS NULL`, did); err != nil {
			return err
		}
		out, err = scanDevice(tx.QueryRow(ctx, deviceSelect+` WHERE id = $1`, did))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "token_rotated", Category: audit.CategorySecurity,
			EntityType: "platform.device", EntityID: did.String(), EntityLabel: out.Name})
	})
	out.DeviceToken = token
	respond(w, r, http.StatusOK, out, err)
}

// ── API keys (FR-INT-06) ──────────────────────────────────────────────────

type APIKey struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	PropertyID *uuid.UUID `json:"propertyId"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RotatedAt  *time.Time `json:"rotatedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	Key        string     `json:"key,omitempty" doc:"Full key, returned only once at creation or rotation"`
}

type APIKeyRequest struct {
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	PropertyID *uuid.UUID `json:"propertyId,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

const apiKeySelect = `SELECT id, name, prefix, scopes, property_id, expires_at, revoked_at, last_used_at, rotated_at, created_at FROM platform.api_keys`

func scanAPIKey(row pgx.Row) (APIKey, error) {
	var k APIKey
	err := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.Scopes, &k.PropertyID, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &k.RotatedAt, &k.CreatedAt)
	return k, err
}

func newAPIKey() (prefix, full string) {
	prefix = strings.ToLower(secret.RandomToken(6))
	prefix = strings.NewReplacer("_", "x", "-", "y").Replace(prefix)
	return prefix, "ock_" + prefix + "_" + secret.RandomToken(32)
}

func (s *Service) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out []APIKey
	err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, apiKeySelect+` ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			k, err := scanAPIKey(rows)
			if err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	if out == nil {
		out = []APIKey{}
	}
	respond(w, r, http.StatusOK, httpx.Page[APIKey]{Items: out}, err)
}

func (s *Service) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var req APIKeyRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	if strings.TrimSpace(req.Name) == "" || len(req.Scopes) == 0 {
		respond(w, r, 0, nil, errs.Validation("invalid_api_key", "name and at least one scope are required",
			errs.Field("name", "required", "name is required"), errs.Field("scopes", "required", "at least one scope")))
		return
	}
	req.Scopes = dedupe(req.Scopes)
	if err := s.checkGrantable(ctx, req.Scopes); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	if req.ExpiresAt != nil && req.ExpiresAt.Before(clock.Now()) {
		respond(w, r, 0, nil, errs.Validation("invalid_expiry", "expiry must be in the future", errs.Field("expiresAt", "past", "must be in the future")))
		return
	}
	prefix, full := newAPIKey()
	var out APIKey
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		kid := id.New()
		uid := id.Ptr(authz.From(ctx).UserID)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.api_keys (id, name, prefix, secret_hash, scopes, property_id, expires_at, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`, kid, req.Name, prefix, secret.HashToken(full), req.Scopes, req.PropertyID, req.ExpiresAt, uid); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Validation("property_invalid", "property not found", errs.Field("propertyId", "invalid", "property not found"))
			}
			return err
		}
		var err error
		out, err = scanAPIKey(tx.QueryRow(ctx, apiKeySelect+` WHERE id = $1`, kid))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, Category: audit.CategorySecurity,
			EntityType: "platform.api_key", EntityID: kid.String(), EntityLabel: req.Name, After: out})
	})
	out.Key = full
	respond(w, r, http.StatusCreated, out, err)
}

func (s *Service) apiKeyAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kid, err := httpx.PathUUID(r, "id")
		if err != nil {
			respond(w, r, 0, nil, err)
			return
		}
		ctx := r.Context()
		var out APIKey
		var full string
		err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			before, err := scanAPIKey(tx.QueryRow(ctx, apiKeySelect+` WHERE id = $1 FOR UPDATE`, kid))
			if dbtx.IsNoRows(err) {
				return errs.NotFound("API key")
			}
			if err != nil {
				return err
			}
			if before.RevokedAt != nil {
				return errs.Conflict("api_key_revoked", "API key is already revoked")
			}
			switch action {
			case "rotate":
				var prefix string
				prefix, full = newAPIKey()
				_, err = tx.Exec(ctx, `UPDATE platform.api_keys SET prefix = $2, secret_hash = $3, rotated_at = now() WHERE id = $1`, kid, prefix, secret.HashToken(full))
			default:
				_, err = tx.Exec(ctx, `UPDATE platform.api_keys SET revoked_at = now() WHERE id = $1`, kid)
			}
			if err != nil {
				return err
			}
			out, err = scanAPIKey(tx.QueryRow(ctx, apiKeySelect+` WHERE id = $1`, kid))
			if err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "api_key_" + action, Category: audit.CategorySecurity,
				EntityType: "platform.api_key", EntityID: kid.String(), EntityLabel: out.Name, Before: before, After: out})
		})
		out.Key = full
		respond(w, r, http.StatusOK, out, err)
	}
}
