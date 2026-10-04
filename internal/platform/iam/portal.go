package iam

// Member Portal sign-in (FR-APP-01, P0 Open Question #7): besides e-mail +
// password, members can sign in with a one-time code sent by e-mail or
// WhatsApp. Accounts of members migrated from Rhapsody are activated with
// an invitation link to the Member Portal (EnsurePortalUser +
// SendActivation, called by the membership module).

import (
	"context"
	"crypto/rand"
	"math/big"
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
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/notify"
)

// OTP policy.
const (
	LoginCodeTTL      = 10 * time.Minute
	LoginCodeAttempts = 5
)

type OTPRequest struct {
	Email   string `json:"email"`
	Channel string `json:"channel,omitempty" enum:"email,whatsapp" doc:"Default email; whatsapp needs a phone number on the account"`
}

type OTPVerifyRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func randomDigits(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		v, _ := rand.Int(rand.Reader, big.NewInt(10))
		b.WriteByte(byte('0' + v.Int64()))
	}
	return b.String()
}

// handleOTPRequest always answers 202 so it cannot be used to discover
// accounts. Codes are only issued to active accounts whose roles do not
// require MFA (Member & Guest Portal users).
func (s *Service) handleOTPRequest(w http.ResponseWriter, r *http.Request) {
	var req OTPRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if err := s.rateLimit("otp-ip:"+reqctx.GetMeta(ctx).IP, 10, time.Minute); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	if err := s.rateLimit("otp-email:"+email, 3, 5*time.Minute); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	channel := req.Channel
	if channel == "" {
		channel = "email"
	}
	if channel != "email" && channel != "whatsapp" {
		respond(w, r, 0, nil, errs.Validation("invalid_channel", "channel must be email or whatsapp"))
		return
	}
	err := s.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		var uid uuid.UUID
		var name, status string
		var phone *string
		err := tx.QueryRow(ctx, `SELECT id, full_name, status, phone FROM platform.users WHERE email = $1`, email).Scan(&uid, &name, &status, &phone)
		if dbtx.IsNoRows(err) || (err == nil && status != "active") {
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "login_code_refused", Category: audit.CategorySecurity,
				EntityType: "platform.user", ActorName: mask.Email(email), Metadata: map[string]any{"reason": "unknown_or_inactive"}})
		}
		if err != nil {
			return err
		}
		_, roleMFA, err := s.loadAssignments(ctx, uid)
		if err != nil {
			return err
		}
		if roleMFA {
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "login_code_refused", Category: audit.CategorySecurity,
				EntityType: "platform.user", EntityID: uid.String(), EntityLabel: name, ActorName: name, Metadata: map[string]any{"reason": "mfa_role"}})
		}
		code := randomDigits(6)
		if _, err := tx.Exec(ctx, `UPDATE platform.login_codes SET used_at = now() WHERE user_id = $1 AND used_at IS NULL`, uid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.login_codes (id, user_id, channel, code_hash, expires_at) VALUES ($1,$2,$3,$4,$5)`,
			id.New(), uid, channel, secret.HashToken(email+":"+code), clock.Now().Add(LoginCodeTTL)); err != nil {
			return err
		}
		ch := []string{notify.ChannelEmail}
		if channel == "whatsapp" && phone != nil && *phone != "" {
			ch = []string{notify.ChannelWhatsApp}
		}
		if err := s.Notify.Send(ctx, tx, notify.Message{Event: "auth.login_code", Category: "security", UserIDs: []uuid.UUID{uid}, Mandatory: true,
			Channels: ch, Data: map[string]any{"name": name, "code": code, "expiresInMinutes": int(LoginCodeTTL.Minutes())}}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "login_code_issued", Category: audit.CategorySecurity,
			EntityType: "platform.user", EntityID: uid.String(), EntityLabel: name, ActorName: name, Metadata: map[string]any{"channel": ch[0]}})
	})
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// handleOTPVerify exchanges a valid code for a session.
func (s *Service) handleOTPVerify(w http.ResponseWriter, r *http.Request) {
	var req OTPVerifyRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	meta := reqctx.GetMeta(ctx)
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if err := s.rateLimit("otp-verify:"+meta.IP, 20, time.Minute); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var resp LoginResponse
	var token string
	var outcome error
	err := s.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		var cid, uid uuid.UUID
		var hash, name string
		var attempts int
		err := tx.QueryRow(ctx, `SELECT c.id, c.user_id, c.code_hash, c.attempts, u.full_name FROM platform.login_codes c
			JOIN platform.users u ON u.id = c.user_id
			WHERE u.email = $1 AND u.status = 'active' AND c.used_at IS NULL AND c.expires_at > now()
			ORDER BY c.created_at DESC LIMIT 1 FOR UPDATE OF c`, email).Scan(&cid, &uid, &hash, &attempts, &name)
		if dbtx.IsNoRows(err) {
			outcome = errs.Validation("login_code_invalid", "the code is invalid or has expired")
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionLoginFailed, Category: audit.CategorySecurity,
				EntityType: "platform.user", ActorName: mask.Email(email), Metadata: map[string]any{"reason": "no_login_code"}})
		}
		if err != nil {
			return err
		}
		if secret.HashToken(email+":"+strings.TrimSpace(req.Code)) != hash {
			attempts++
			used := "NULL"
			if attempts >= LoginCodeAttempts {
				used = "now()"
			}
			if _, err := tx.Exec(ctx, `UPDATE platform.login_codes SET attempts = $2, used_at = `+used+` WHERE id = $1`, cid, attempts); err != nil {
				return err
			}
			outcome = errs.Validation("login_code_invalid", "the code is invalid or has expired")
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionLoginFailed, Category: audit.CategorySecurity,
				EntityType: "platform.user", EntityID: uid.String(), EntityLabel: name, ActorName: name, Metadata: map[string]any{"reason": "bad_login_code"}})
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.login_codes SET used_at = now() WHERE id = $1`, cid); err != nil {
			return err
		}
		token = newSessionToken()
		sid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO platform.sessions (id, user_id, token_hash, kind, ip, user_agent, expires_at, mfa_verified)
			VALUES ($1, $2, $3, 'web', $4, $5, $6, false)`, sid, uid, secret.HashToken(token), meta.IP, meta.UserAgent, clock.Now().Add(s.Cfg.SessionTTL)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.users SET last_login_at = now(), failed_login_count = 0 WHERE id = $1`, uid); err != nil {
			return err
		}
		resp = LoginResponse{User: UserSummary{ID: uid, Email: email, FullName: name}}
		actx := authz.WithPrincipal(ctx, &authz.Principal{Kind: authz.ActorUser, UserID: uid, Name: name, SessionID: sid})
		return audit.Record(actx, tx, audit.Entry{Module: "platform", Action: audit.ActionLoginSucceeded, Category: audit.CategorySecurity,
			EntityType: "platform.user", EntityID: uid.String(), EntityLabel: name, Metadata: map[string]any{"method": "login_code"}})
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

// PortalUser describes a Member & Guest Portal account.
type PortalUser struct {
	Email      string
	Name       string
	Phone      string
	Locale     string
	RoleCode   string // member | guest
	PropertyID uuid.UUID
}

// EnsurePortalUser finds or creates the user for a portal account and makes
// sure it holds the portal role at the property. created reports a new user.
func (s *Service) EnsurePortalUser(ctx context.Context, tx pgx.Tx, u PortalUser) (uuid.UUID, bool, error) {
	email := strings.ToLower(strings.TrimSpace(u.Email))
	if email == "" || !strings.Contains(email, "@") {
		return uuid.Nil, false, errs.Validation("email_required", "a valid e-mail address is required for the Member Portal",
			errs.Field("email", "required", "e-mail is required"))
	}
	if u.RoleCode == "" {
		u.RoleCode = "member"
	}
	var uid uuid.UUID
	created := false
	err := tx.QueryRow(ctx, `SELECT id FROM platform.users WHERE email = $1`, email).Scan(&uid)
	if dbtx.IsNoRows(err) {
		uid = id.New()
		created = true
		loc := u.Locale
		if loc != "en" && loc != "id" {
			loc = "id"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users (id, email, full_name, phone, locale, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$6)`, uid, email, u.Name, nullable(u.Phone), loc, id.Ptr(actor(ctx))); err != nil {
			return uuid.Nil, false, err
		}
	} else if err != nil {
		return uuid.Nil, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.role_assignments (id, user_id, role_id, property_id, created_by)
		SELECT $1, $2, r.id, $3, $5 FROM platform.roles r WHERE r.code = $4 ON CONFLICT DO NOTHING`,
		id.New(), uid, u.PropertyID, u.RoleCode, id.Ptr(actor(ctx))); err != nil {
		return uuid.Nil, false, err
	}
	if created {
		if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, Category: audit.CategorySecurity,
			EntityType: "platform.user", EntityID: uid.String(), EntityLabel: u.Name, PropertyID: &u.PropertyID,
			After: map[string]any{"email": mask.Email(email), "role": u.RoleCode}}); err != nil {
			return uuid.Nil, false, err
		}
	}
	return uid, created, nil
}

// SendActivation e-mails the portal activation link (set password) to a
// portal user — used for members migrated from Rhapsody and new members.
func (s *Service) SendActivation(ctx context.Context, tx pgx.Tx, uid uuid.UUID, name string) error {
	token := secret.RandomToken(32)
	if _, err := tx.Exec(ctx, `UPDATE platform.password_reset_tokens SET used_at = now() WHERE user_id = $1 AND used_at IS NULL`, uid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.password_reset_tokens (id, user_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		id.New(), uid, secret.HashToken(token), clock.Now().Add(InviteTokenTTL)); err != nil {
		return err
	}
	link := s.Cfg.MemberPortalURL + "/reset-password?token=" + token
	return s.Notify.Send(ctx, tx, notify.Message{Event: "auth.portal_activation", Category: "security", UserIDs: []uuid.UUID{uid}, Mandatory: true,
		Channels: []string{notify.ChannelEmail, notify.ChannelWhatsApp},
		Data:     map[string]any{"name": name, "link": link, "expiresInHours": int(InviteTokenTTL.Hours())}})
}

func actor(ctx context.Context) uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return p.UserID
	}
	return uuid.Nil
}

func nullable(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
