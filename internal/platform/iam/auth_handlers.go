package iam

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/png"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/iam/password"
	"oneclub/internal/platform/notify"
)

// ── DTOs ────────────────────────────────────────────────────────────────────

type LoginRequest struct {
	Email    string `json:"email" format:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	MFARequired            bool        `json:"mfaRequired"`
	MFAEnrolled            bool        `json:"mfaEnrolled"`
	PasswordChangeRequired bool        `json:"passwordChangeRequired"`
	User                   UserSummary `json:"user"`
}

type UserSummary struct {
	ID       uuid.UUID `json:"id"`
	Email    string    `json:"email"`
	FullName string    `json:"fullName"`
}

type MFASetupResponse struct {
	Secret     string `json:"secret"`
	OtpauthURL string `json:"otpauthUrl"`
	QRCodePNG  string `json:"qrCodePng" doc:"data: URL of a QR code image"`
}

type MFAVerifyRequest struct {
	Code string `json:"code"`
}

type PasswordResetRequest struct {
	Email string `json:"email" format:"email"`
}

type PasswordResetConfirm struct {
	Token       string `json:"token"`
	NewPassword string `json:"newPassword"`
}

type PasswordChangeRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type DeviceLoginRequest struct {
	DeviceToken string `json:"deviceToken"`
	Email       string `json:"email" format:"email"`
	PIN         string `json:"pin"`
}

type PropertyRef struct {
	ID       uuid.UUID `json:"id"`
	Code     string    `json:"code"`
	Name     string    `json:"name"`
	Timezone *string   `json:"timezone"`
}

type RoleRef struct {
	Code       string     `json:"code"`
	Name       string     `json:"name"`
	PropertyID *uuid.UUID `json:"propertyId"`
}

type MeResponse struct {
	ID                     uuid.UUID     `json:"id"`
	Email                  string        `json:"email"`
	FullName               string        `json:"fullName"`
	Kind                   string        `json:"kind" enum:"user,device,api_key"`
	Locale                 string        `json:"locale" enum:"en,id"`
	Theme                  *string       `json:"theme"`
	MFAEnabled             bool          `json:"mfaEnabled"`
	MFARequired            bool          `json:"mfaRequired"`
	MFAPending             bool          `json:"mfaPending"`
	PasswordChangeRequired bool          `json:"passwordChangeRequired"`
	AllProperties          bool          `json:"allProperties"`
	Properties             []PropertyRef `json:"properties"`
	Roles                  []RoleRef     `json:"roles"`
	Permissions            []string      `json:"permissions" doc:"Permissions at the active property (X-Property-Id) or across all properties"`
	Shells                 []string      `json:"shells" doc:"Application surfaces this user may open"`
	DeviceID               *uuid.UUID    `json:"deviceId"`
}

type PreferencesRequest struct {
	Locale *string `json:"locale" enum:"en,id"`
	Theme  *string `json:"theme" enum:"light,dark,system"`
}

// ── helpers ────────────────────────────────────────────────────────────────

// Secure comes from config: always true in deployed environments, false only
// for plain-http local development.
func (s *Service) setCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is config-driven, see above
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds()),
	})
}

func (s *Service) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is config-driven, see setCookie
		Name: SessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func newSessionToken() string { return "ocs_" + secret.RandomToken(32) }

var errBadCredentials = func() error {
	e := errs.Unauthorized("invalid e-mail or password")
	e.Code = "invalid_credentials"
	return e
}

// ── Login (FR-IAM-02, FR-IAM-04) ──────────────────────────────────────────

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	meta := reqctx.GetMeta(ctx)
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if err := s.rateLimit("login-ip:"+meta.IP, 30, time.Minute); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	if err := s.rateLimit("login-email:"+email, 10, time.Minute); err != nil {
		respond(w, r, 0, nil, err)
		return
	}

	var resp LoginResponse
	var token string
	var outcome error
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var (
			uid                    uuid.UUID
			fullName, status       string
			hash                   *string
			failed                 int
			lockedUntil            *time.Time
			mfaEnabled, mustChange bool
		)
		err := tx.QueryRow(ctx, `
			SELECT id, full_name, status, password_hash, failed_login_count, locked_until, mfa_enabled, must_change_password
			FROM platform.users WHERE email = $1 FOR UPDATE`, email).
			Scan(&uid, &fullName, &status, &hash, &failed, &lockedUntil, &mfaEnabled, &mustChange)
		if dbtx.IsNoRows(err) {
			password.VerifyDummy(req.Password)
			outcome = errBadCredentials()
			return audit.Record(ctx, tx, audit.Entry{
				Module: "platform", Action: audit.ActionLoginFailed, Category: audit.CategorySecurity,
				EntityType: "platform.user", ActorName: mask.Email(email),
				Metadata: map[string]any{"reason": "unknown_email"},
			})
		}
		if err != nil {
			return err
		}
		failAudit := func(reason string) error {
			return audit.Record(ctx, tx, audit.Entry{
				Module: "platform", Action: audit.ActionLoginFailed, Category: audit.CategorySecurity,
				EntityType: "platform.user", EntityID: uid.String(), EntityLabel: fullName, ActorName: fullName,
				Metadata: map[string]any{"reason": reason},
			})
		}
		if lockedUntil != nil && lockedUntil.After(clock.Now()) {
			outcome = errs.Locked("account is temporarily locked after repeated failed attempts")
			return failAudit("locked")
		}
		ok := false
		if hash != nil {
			ok, _ = password.Verify(req.Password, *hash)
		} else {
			password.VerifyDummy(req.Password)
		}
		if !ok {
			failed++
			outcome = errBadCredentials()
			if failed >= MaxFailedLogins {
				until := clock.Now().Add(LockoutDuration)
				if _, err := tx.Exec(ctx, `UPDATE platform.users SET failed_login_count = 0, locked_until = $2 WHERE id = $1`, uid, until); err != nil {
					return err
				}
				if err := audit.Record(ctx, tx, audit.Entry{
					Module: "platform", Action: audit.ActionAccountLocked, Category: audit.CategorySecurity,
					EntityType: "platform.user", EntityID: uid.String(), EntityLabel: fullName, ActorName: fullName,
					Metadata: map[string]any{"lockedUntil": until},
				}); err != nil {
					return err
				}
			} else if _, err := tx.Exec(ctx, `UPDATE platform.users SET failed_login_count = $2 WHERE id = $1`, uid, failed); err != nil {
				return err
			}
			return failAudit("bad_password")
		}
		if status != "active" {
			outcome = errBadCredentials()
			return failAudit("inactive")
		}

		token = newSessionToken()
		sid := id.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.sessions (id, user_id, token_hash, kind, ip, user_agent, expires_at)
			VALUES ($1, $2, $3, 'web', $4, $5, $6)`,
			sid, uid, secret.HashToken(token), meta.IP, meta.UserAgent, clock.Now().Add(s.Cfg.SessionTTL)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.users SET failed_login_count = 0, locked_until = NULL, last_login_at = now() WHERE id = $1`, uid); err != nil {
			return err
		}
		_, roleMFA, err := s.loadAssignments(ctx, uid)
		if err != nil {
			return err
		}
		resp = LoginResponse{
			MFARequired: mfaEnabled || roleMFA, MFAEnrolled: mfaEnabled, PasswordChangeRequired: mustChange,
			User: UserSummary{ID: uid, Email: email, FullName: fullName},
		}
		actx := authz.WithPrincipal(ctx, &authz.Principal{Kind: authz.ActorUser, UserID: uid, Name: fullName, SessionID: sid})
		return audit.Record(actx, tx, audit.Entry{
			Module: "platform", Action: audit.ActionLoginSucceeded, Category: audit.CategorySecurity,
			EntityType: "platform.user", EntityID: uid.String(), EntityLabel: fullName,
			Metadata: map[string]any{"mfaRequired": resp.MFARequired},
		})
	})
	if err == nil {
		err = outcome
	}
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	s.setCookie(w, token, s.Cfg.SessionTTL)
	httpx.JSON(w, http.StatusOK, resp)
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authz.From(ctx)
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now(), revoked_reason = 'logout' WHERE id = $1 AND revoked_at IS NULL`, p.SessionID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Module: "platform", Action: audit.ActionLogout, Category: audit.CategorySecurity,
			EntityType: "platform.session", EntityID: p.SessionID.String(), EntityLabel: p.Name,
		})
	})
	s.clearCookie(w)
	respond(w, r, 0, nil, err)
}

// ── Me ─────────────────────────────────────────────────────────────────────

// Shells derives application surfaces from permissions (FR-SH-02).
func Shells(p *authz.Principal) []string {
	var out []string
	for _, sh := range []struct{ code, perm string }{
		{"backoffice", catalog.ShellBackOffice},
		{"management", catalog.ManagementView},
		{"ops", catalog.ShellOps},
		{"member", catalog.ShellMemberPortal},
		{"platform-admin", catalog.ShellPlatformAdmin},
	} {
		if p.Can(sh.perm, nil) {
			out = append(out, sh.code)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// AccessibleProperties lists active properties the principal may switch to.
func (s *Service) AccessibleProperties(ctx context.Context, p *authz.Principal) ([]PropertyRef, error) {
	var out []PropertyRef
	err := s.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, code, name, timezone FROM platform.properties
			WHERE status = 'active' AND archived_at IS NULL ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pr PropertyRef
			if err := rows.Scan(&pr.ID, &pr.Code, &pr.Name, &pr.Timezone); err != nil {
				return err
			}
			if p.CanAccessProperty(pr.ID) {
				out = append(out, pr)
			}
		}
		return rows.Err()
	})
	if out == nil {
		out = []PropertyRef{}
	}
	return out, err
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authz.From(ctx)
	if p.Kind == authz.ActorAPIKey {
		respond(w, r, 0, nil, errs.Forbidden("not available for API keys"))
		return
	}
	var theme *string
	if err := s.DB.Primary.QueryRow(ctx, `SELECT theme FROM platform.users WHERE id = $1`, p.UserID).Scan(&theme); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	props, err := s.AccessibleProperties(ctx, p)
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	all, _ := p.PropertiesFor("")
	var active *uuid.UUID
	if pid, ok := reqctx.Property(ctx); ok {
		active = &pid
	}
	roles := []RoleRef{}
	for _, a := range p.Assignments {
		roles = append(roles, RoleRef{Code: a.RoleCode, Name: a.RoleName, PropertyID: a.PropertyID})
	}
	perms := p.Permissions(active)
	if p.MFAPending || p.PasswordChangeRequired {
		perms = []string{}
	}
	locale := p.Locale
	if locale == "" {
		locale = reqctx.GetMeta(ctx).Locale
	}
	shells := Shells(p)
	if p.MFAPending || p.PasswordChangeRequired {
		shells = []string{}
	}
	httpx.JSON(w, http.StatusOK, MeResponse{
		ID: p.UserID, Email: p.Email, FullName: p.Name, Kind: string(p.Kind), Locale: locale, Theme: theme,
		MFAEnabled: p.MFAEnabled, MFARequired: p.MFARequired, MFAPending: p.MFAPending,
		PasswordChangeRequired: p.PasswordChangeRequired, AllProperties: all, Properties: props,
		Roles: roles, Permissions: perms, Shells: shells, DeviceID: p.DeviceID,
	})
}

func (s *Service) handlePreferences(w http.ResponseWriter, r *http.Request) {
	var req PreferencesRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	if req.Locale != nil && *req.Locale != "en" && *req.Locale != "id" {
		respond(w, r, 0, nil, errs.Validation("invalid_locale", "locale must be en or id", errs.Field("locale", "invalid", "must be en or id")))
		return
	}
	if req.Theme != nil && *req.Theme != "light" && *req.Theme != "dark" && *req.Theme != "system" {
		respond(w, r, 0, nil, errs.Validation("invalid_theme", "theme must be light, dark or system", errs.Field("theme", "invalid", "must be light, dark or system")))
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var bl, bt *string
		if err := tx.QueryRow(ctx, `SELECT locale, theme FROM platform.users WHERE id = $1`, p.UserID).Scan(&bl, &bt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.users SET locale = coalesce($2, locale), theme = coalesce($3, theme), updated_by = $1 WHERE id = $1`,
			p.UserID, req.Locale, req.Theme); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Module: "platform", Action: audit.ActionUpdate, EntityType: "platform.user_preferences", EntityID: p.UserID.String(),
			EntityLabel: p.Name, Before: map[string]any{"locale": bl, "theme": bt}, After: req,
		})
	})
	respond(w, r, 0, nil, err)
}

// ── MFA (FR-IAM-03) ────────────────────────────────────────────────────────

func (s *Service) issuer(ctx context.Context) string {
	var name string
	if err := s.DB.Primary.QueryRow(ctx, `SELECT coalesce(branding->>'appName', name) FROM platform.instance`).Scan(&name); err != nil || name == "" {
		return "OneClub"
	}
	return name
}

func (s *Service) handleMFASetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authz.From(ctx)
	if p.Kind != authz.ActorUser {
		respond(w, r, 0, nil, errs.Forbidden("MFA is managed for user accounts only"))
		return
	}
	if p.MFAEnabled {
		respond(w, r, 0, nil, errs.Conflict("mfa_already_enabled", "MFA is already enabled; ask an administrator to reset it"))
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: s.issuer(ctx), AccountName: p.Email, Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	sealed, err := s.Box.Seal([]byte(key.Secret()))
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var qr string
	if img, err := key.Image(240, 240); err == nil {
		var buf bytes.Buffer
		if png.Encode(&buf, img) == nil {
			qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
		}
	}
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.users SET mfa_pending_secret_enc = $2 WHERE id = $1`, p.UserID, sealed); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Module: "platform", Action: "mfa_setup_started", Category: audit.CategorySecurity,
			EntityType: "platform.user", EntityID: p.UserID.String(), EntityLabel: p.Name,
		})
	})
	respond(w, r, http.StatusOK, MFASetupResponse{Secret: key.Secret(), OtpauthURL: key.URL(), QRCodePNG: qr}, err)
}

func validTOTP(code, secretStr string) bool {
	ok, _ := totp.ValidateCustom(strings.TrimSpace(code), secretStr, clock.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	return ok
}

func (s *Service) handleMFAVerify(w http.ResponseWriter, r *http.Request) {
	var req MFAVerifyRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	if err := s.rateLimit("mfa:"+p.SessionID.String(), 5, 5*time.Minute); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var outcome error
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var enabled bool
		var cur, pending []byte
		if err := tx.QueryRow(ctx, `SELECT mfa_enabled, mfa_secret_enc, mfa_pending_secret_enc FROM platform.users WHERE id = $1 FOR UPDATE`, p.UserID).
			Scan(&enabled, &cur, &pending); err != nil {
			return err
		}
		sealed := cur
		enrolling := !enabled
		if enrolling {
			sealed = pending
		}
		if sealed == nil {
			outcome = errs.Precondition("start MFA setup first")
			return nil
		}
		plain, err := s.Box.Open(sealed)
		if err != nil {
			return err
		}
		if !validTOTP(req.Code, string(plain)) {
			e := errs.Validation("mfa_code_invalid", "invalid authentication code", errs.Field("code", "invalid", "invalid or expired code"))
			outcome = e
			return audit.Record(ctx, tx, audit.Entry{
				Module: "platform", Action: audit.ActionMFAFailed, Category: audit.CategorySecurity,
				EntityType: "platform.user", EntityID: p.UserID.String(), EntityLabel: p.Name,
			})
		}
		if enrolling {
			if _, err := tx.Exec(ctx, `UPDATE platform.users SET mfa_enabled = true, mfa_secret_enc = mfa_pending_secret_enc, mfa_pending_secret_enc = NULL WHERE id = $1`, p.UserID); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Entry{
				Module: "platform", Action: audit.ActionMFAEnrolled, Category: audit.CategorySecurity,
				EntityType: "platform.user", EntityID: p.UserID.String(), EntityLabel: p.Name,
			}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET mfa_verified = true WHERE id = $1`, p.SessionID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Module: "platform", Action: audit.ActionMFAVerified, Category: audit.CategorySecurity,
			EntityType: "platform.session", EntityID: p.SessionID.String(), EntityLabel: p.Name,
		})
	})
	if err == nil {
		err = outcome
	}
	respond(w, r, 0, nil, err)
}

// ── Password reset & change (FR-IAM-04) ──────────────────────────────────

func (s *Service) handlePasswordReset(w http.ResponseWriter, r *http.Request) {
	var req PasswordResetRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	if err := s.rateLimit("reset-ip:"+reqctx.GetMeta(ctx).IP, 10, time.Minute); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var uid uuid.UUID
		var name string
		err := tx.QueryRow(ctx, `SELECT id, full_name FROM platform.users WHERE email = $1 AND status = 'active'`, email).Scan(&uid, &name)
		if dbtx.IsNoRows(err) {
			// Same response for unknown addresses (no account enumeration).
			return audit.Record(ctx, tx, audit.Entry{
				Module: "platform", Action: "password_reset_requested", Category: audit.CategorySecurity,
				EntityType: "platform.user", ActorName: mask.Email(email), Metadata: map[string]any{"known": false},
			})
		}
		if err != nil {
			return err
		}
		if err := s.issueResetToken(ctx, tx, uid, name, "auth.password_reset", ResetTokenTTL); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Module: "platform", Action: "password_reset_requested", Category: audit.CategorySecurity,
			EntityType: "platform.user", EntityID: uid.String(), EntityLabel: name, ActorName: name,
		})
	})
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// issueResetToken stores a one-time token and e-mails the link.
func (s *Service) issueResetToken(ctx context.Context, tx pgx.Tx, uid uuid.UUID, name, event string, ttl time.Duration) error {
	token := secret.RandomToken(32)
	if _, err := tx.Exec(ctx, `UPDATE platform.password_reset_tokens SET used_at = now() WHERE user_id = $1 AND used_at IS NULL`, uid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.password_reset_tokens (id, user_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		id.New(), uid, secret.HashToken(token), clock.Now().Add(ttl)); err != nil {
		return err
	}
	link := s.Cfg.PublicBaseURL + "/reset-password?token=" + token
	return s.Notify.Send(ctx, tx, notify.Message{
		Event: event, Category: "security", UserIDs: []uuid.UUID{uid}, Mandatory: true,
		Channels: []string{notify.ChannelEmail},
		Data:     map[string]any{"name": name, "link": link, "expiresInMinutes": int(ttl.Minutes())},
	})
}

func (s *Service) handlePasswordResetConfirm(w http.ResponseWriter, r *http.Request) {
	var req PasswordResetConfirm
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var tid, uid uuid.UUID
		var email, name string
		err := tx.QueryRow(ctx, `
			SELECT t.id, u.id, u.email, u.full_name FROM platform.password_reset_tokens t
			JOIN platform.users u ON u.id = t.user_id
			WHERE t.token_hash = $1 AND t.used_at IS NULL AND t.expires_at > now() AND u.status = 'active'
			FOR UPDATE OF t`, secret.HashToken(req.Token)).Scan(&tid, &uid, &email, &name)
		if dbtx.IsNoRows(err) {
			return errs.Validation("reset_token_invalid", "the reset link is invalid or has expired")
		}
		if err != nil {
			return err
		}
		if err := password.CheckPolicy(req.NewPassword, email); err != nil {
			return err
		}
		h, err := password.Hash(req.NewPassword)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.password_reset_tokens SET used_at = now() WHERE id = $1`, tid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.users SET password_hash = $2, password_changed_at = now(), must_change_password = false,
			failed_login_count = 0, locked_until = NULL WHERE id = $1`, uid, h); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now(), revoked_reason = 'password_reset' WHERE user_id = $1 AND revoked_at IS NULL`, uid); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Module: "platform", Action: audit.ActionPasswordReset, Category: audit.CategorySecurity,
			EntityType: "platform.user", EntityID: uid.String(), EntityLabel: name, ActorName: name,
		})
	})
	respond(w, r, 0, nil, err)
}

func (s *Service) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	var req PasswordChangeRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	if p.MFAPending {
		respond(w, r, 0, nil, errs.MFARequired("complete multi-factor authentication first"))
		return
	}
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var hash *string
		if err := tx.QueryRow(ctx, `SELECT password_hash FROM platform.users WHERE id = $1 FOR UPDATE`, p.UserID).Scan(&hash); err != nil {
			return err
		}
		if hash == nil {
			return errs.Validation("current_password_invalid", "current password is incorrect", errs.Field("currentPassword", "invalid", "incorrect password"))
		}
		if ok, _ := password.Verify(req.CurrentPassword, *hash); !ok {
			return errs.Validation("current_password_invalid", "current password is incorrect", errs.Field("currentPassword", "invalid", "incorrect password"))
		}
		if req.CurrentPassword == req.NewPassword {
			return errs.Validation("password_unchanged", "new password must differ", errs.Field("newPassword", "unchanged", "must differ from the current password"))
		}
		if err := password.CheckPolicy(req.NewPassword, p.Email); err != nil {
			return err
		}
		h, err := password.Hash(req.NewPassword)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.users SET password_hash = $2, password_changed_at = now(), must_change_password = false WHERE id = $1`, p.UserID, h); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now(), revoked_reason = 'password_changed'
			WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`, p.UserID, p.SessionID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Module: "platform", Action: audit.ActionPasswordChanged, Category: audit.CategorySecurity,
			EntityType: "platform.user", EntityID: p.UserID.String(), EntityLabel: p.Name,
		})
	})
	respond(w, r, 0, nil, err)
}

// ── Device + PIN login (FR-IAM-09) ────────────────────────────────────────

func (s *Service) handleDeviceLogin(w http.ResponseWriter, r *http.Request) {
	var req DeviceLoginRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	if err := s.rateLimit("pin-ip:"+reqctx.GetMeta(ctx).IP, 30, time.Minute); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	var token string
	var resp LoginResponse
	var outcome error
	err := s.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		var devID, propID uuid.UUID
		var devName string
		err := tx.QueryRow(ctx, `SELECT id, property_id, name FROM platform.devices WHERE token_hash = $1 AND status = 'active'`,
			secret.HashToken(req.DeviceToken)).Scan(&devID, &propID, &devName)
		if dbtx.IsNoRows(err) {
			outcome = errs.Unauthorized("device is not registered")
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionLoginFailed, Category: audit.CategorySecurity,
				EntityType: "platform.device", ActorName: mask.Email(email), Metadata: map[string]any{"reason": "unknown_device"}})
		}
		if err != nil {
			return err
		}
		var uid uuid.UUID
		var name, status string
		var pinHash *string
		var lockedUntil *time.Time
		var failed int
		err = tx.QueryRow(ctx, `SELECT id, full_name, status, pin_hash, locked_until, failed_login_count FROM platform.users WHERE email = $1 FOR UPDATE`, email).
			Scan(&uid, &name, &status, &pinHash, &lockedUntil, &failed)
		if dbtx.IsNoRows(err) {
			password.VerifyDummy(req.PIN)
			outcome = errBadCredentials()
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionLoginFailed, Category: audit.CategorySecurity,
				EntityType: "platform.user", ActorName: mask.Email(email), PropertyID: &propID, Metadata: map[string]any{"reason": "unknown_email", "deviceId": devID}})
		}
		if err != nil {
			return err
		}
		fail := func(reason string) error {
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionLoginFailed, Category: audit.CategorySecurity,
				EntityType: "platform.user", EntityID: uid.String(), EntityLabel: name, ActorName: name, PropertyID: &propID,
				Metadata: map[string]any{"reason": reason, "deviceId": devID}})
		}
		if lockedUntil != nil && lockedUntil.After(clock.Now()) {
			outcome = errs.Locked("account is temporarily locked after repeated failed attempts")
			return fail("locked")
		}
		ok := false
		if pinHash != nil {
			ok, _ = password.Verify(req.PIN, *pinHash)
		}
		if !ok || status != "active" {
			failed++
			outcome = errBadCredentials()
			if failed >= MaxFailedLogins {
				if _, err := tx.Exec(ctx, `UPDATE platform.users SET failed_login_count = 0, locked_until = $2 WHERE id = $1`, uid, clock.Now().Add(LockoutDuration)); err != nil {
					return err
				}
			} else if _, err := tx.Exec(ctx, `UPDATE platform.users SET failed_login_count = $2 WHERE id = $1`, uid, failed); err != nil {
				return err
			}
			return fail("bad_pin")
		}
		assignments, _, err := s.loadAssignments(ctx, uid)
		if err != nil {
			return err
		}
		pr := &authz.Principal{Kind: authz.ActorUser, UserID: uid, Assignments: assignments}
		if !pr.CanAccessProperty(propID) || !pr.Can(catalog.ShellOps, &propID) {
			outcome = errs.Forbidden("you have no operational access at this device's property")
			return fail("no_access")
		}
		token = newSessionToken()
		sid := id.New()
		meta := reqctx.GetMeta(ctx)
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.sessions (id, user_id, token_hash, kind, device_id, property_id, mfa_verified, ip, user_agent, expires_at)
			VALUES ($1, $2, $3, 'device', $4, $5, true, $6, $7, $8)`,
			sid, uid, secret.HashToken(token), devID, propID, meta.IP, meta.UserAgent, clock.Now().Add(DeviceShiftTTL)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.users SET failed_login_count = 0, locked_until = NULL, last_login_at = now() WHERE id = $1`, uid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.devices SET last_seen_at = now() WHERE id = $1`, devID); err != nil {
			return err
		}
		resp = LoginResponse{User: UserSummary{ID: uid, Email: email, FullName: name}}
		actx := authz.WithPrincipal(ctx, &authz.Principal{Kind: authz.ActorDevice, UserID: uid, Name: name, SessionID: sid, DeviceID: &devID})
		return audit.Record(actx, tx, audit.Entry{Module: "platform", Action: audit.ActionLoginSucceeded, Category: audit.CategorySecurity,
			EntityType: "platform.user", EntityID: uid.String(), EntityLabel: name, PropertyID: &propID,
			Metadata: map[string]any{"device": devName, "method": "pin"}})
	})
	if err == nil {
		err = outcome
	}
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	s.setCookie(w, token, DeviceShiftTTL)
	httpx.JSON(w, http.StatusOK, resp)
}
