package iam

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/secret"
)

// Authenticate resolves the principal from the session cookie or a bearer
// token (session token or API key). Role assignments are loaded on every
// request, so revoking a role takes effect on the next request without a
// logout (EP-03 AC).
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (*authz.Principal, error) {
	token := ""
	if c, err := r.Cookie(SessionCookie); err == nil {
		token = c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if token == "" {
		return nil, nil
	}
	if strings.HasPrefix(token, "ock_") {
		return s.authenticateAPIKey(ctx, token)
	}
	return s.authenticateSession(ctx, token)
}

func (s *Service) authenticateSession(ctx context.Context, token string) (*authz.Principal, error) {
	hash := secret.HashToken(token)
	var (
		p                                   authz.Principal
		locale                              *string
		status                              string
		mfaEnabled, mfaVerified, mustChange bool
		kind                                string
		deviceID, sessionProperty           *uuid.UUID
		lastSeen                            time.Time
	)
	err := s.DB.Primary.QueryRow(ctx, `
		SELECT u.id, u.email, u.full_name, u.locale, u.status, u.mfa_enabled, u.must_change_password,
		       s.id, s.mfa_verified, s.kind, s.device_id, s.property_id, s.last_seen_at
		FROM platform.sessions s JOIN platform.users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now()`, hash).
		Scan(&p.UserID, &p.Email, &p.Name, &locale, &status, &mfaEnabled, &mustChange,
			&p.SessionID, &mfaVerified, &kind, &deviceID, &sessionProperty, &lastSeen)
	if dbtx.IsNoRows(err) {
		return nil, errs.Unauthorized("session expired or revoked")
	}
	if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, errs.Unauthorized("account is inactive")
	}
	p.Kind = authz.ActorUser
	if locale != nil {
		p.Locale = *locale
	}
	assignments, mfaRoleRequired, err := s.loadAssignments(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	p.Assignments = assignments
	if kind == "device" {
		// Device shift sessions are confined to the device's property
		// (FR-IAM-09); instance-wide roles narrow to that property.
		p.Kind = authz.ActorDevice
		p.DeviceID = deviceID
		p.Assignments = narrowTo(p.Assignments, sessionProperty)
	}
	p.MFAEnabled = mfaEnabled
	p.MFARequired = mfaEnabled || mfaRoleRequired
	p.MFAPending = p.MFARequired && !mfaVerified && kind == "web"
	p.PasswordChangeRequired = mustChange && kind == "web"

	if clock.Now().Sub(lastSeen) > time.Minute {
		_, _ = s.DB.Primary.Exec(ctx, `UPDATE platform.sessions SET last_seen_at = now() WHERE id = $1`, p.SessionID)
	}
	return &p, nil
}

func narrowTo(as []authz.Assignment, property *uuid.UUID) []authz.Assignment {
	if property == nil {
		return nil
	}
	var out []authz.Assignment
	for _, a := range as {
		if a.PropertyID == nil || *a.PropertyID == *property {
			pid := *property
			a.PropertyID = &pid
			out = append(out, a)
		}
	}
	return out
}

// loadAssignments returns the active role assignments with permissions and
// whether any assigned role requires MFA (FR-IAM-03).
func (s *Service) loadAssignments(ctx context.Context, userID uuid.UUID) ([]authz.Assignment, bool, error) {
	rows, err := s.DB.Primary.Query(ctx, `
		SELECT r.id, r.code, r.name, r.mfa_required, ra.property_id,
		       coalesce(array_agg(rp.permission_code) FILTER (WHERE rp.permission_code IS NOT NULL), '{}')
		FROM platform.role_assignments ra
		JOIN platform.roles r ON r.id = ra.role_id AND r.status = 'active'
		LEFT JOIN platform.role_permissions rp ON rp.role_id = r.id
		WHERE ra.user_id = $1 AND (ra.valid_until IS NULL OR ra.valid_until > now())
		GROUP BY r.id, r.code, r.name, r.mfa_required, ra.property_id`, userID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []authz.Assignment
	mfa := false
	for rows.Next() {
		var a authz.Assignment
		var req bool
		var perms []string
		if err := rows.Scan(&a.RoleID, &a.RoleCode, &a.RoleName, &req, &a.PropertyID, &perms); err != nil {
			return nil, false, err
		}
		a.Permissions = make(map[string]struct{}, len(perms))
		for _, p := range perms {
			a.Permissions[p] = struct{}{}
		}
		mfa = mfa || req
		out = append(out, a)
	}
	return out, mfa, rows.Err()
}

// authenticateAPIKey handles "ock_<prefix>_<secret>" bearer tokens
// (FR-INT-06): scopes become the principal's permissions.
func (s *Service) authenticateAPIKey(ctx context.Context, token string) (*authz.Principal, error) {
	parts := strings.SplitN(token, "_", 3)
	if len(parts) != 3 {
		return nil, errs.Unauthorized("invalid API key")
	}
	var (
		keyID      uuid.UUID
		name       string
		hash       string
		scopes     []string
		propertyID *uuid.UUID
		lastUsed   *time.Time
	)
	err := s.DB.Primary.QueryRow(ctx, `
		SELECT id, name, secret_hash, scopes, property_id, last_used_at FROM platform.api_keys
		WHERE prefix = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, parts[1]).
		Scan(&keyID, &name, &hash, &scopes, &propertyID, &lastUsed)
	if dbtx.IsNoRows(err) {
		return nil, errs.Unauthorized("invalid API key")
	}
	if err != nil {
		return nil, err
	}
	if !secret.Equal(hash, secret.HashToken(token)) {
		return nil, errs.Unauthorized("invalid API key")
	}
	perms := make(map[string]struct{}, len(scopes))
	for _, sc := range scopes {
		perms[sc] = struct{}{}
	}
	if lastUsed == nil || clock.Now().Sub(*lastUsed) > time.Minute {
		_, _ = s.DB.Primary.Exec(ctx, `UPDATE platform.api_keys SET last_used_at = now() WHERE id = $1`, keyID)
	}
	return &authz.Principal{
		Kind: authz.ActorAPIKey, APIKeyID: keyID, Name: "API key: " + name,
		Assignments: []authz.Assignment{{RoleCode: "api_key", RoleName: name, PropertyID: propertyID, Permissions: perms}},
	}, nil
}

// PrincipalFor loads a user's principal without a session, for background
// jobs acting on behalf of that user (e.g. report exports run with the
// requester's current permissions).
func (s *Service) PrincipalFor(ctx context.Context, userID uuid.UUID) (*authz.Principal, error) {
	p := &authz.Principal{Kind: authz.ActorUser, UserID: userID}
	var status string
	var locale *string
	err := s.DB.Primary.QueryRow(ctx, `SELECT email, full_name, status, locale FROM platform.users WHERE id = $1`, userID).
		Scan(&p.Email, &p.Name, &status, &locale)
	if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, errs.Forbidden("user is inactive")
	}
	if locale != nil {
		p.Locale = *locale
	}
	p.Assignments, _, err = s.loadAssignments(ctx, userID)
	return p, err
}
