package membership

// Member App sign-up (demo feedback 10 Oct 2026, items 37.1–37.2):
//
//   - a member of the club registers the account with the member no. and one
//     detail on file (date of birth, phone or e-mail) — no invitation e-mail
//     needed;
//   - a guest registers as a non-member (name, phone, e-mail) and books with
//     the guest rates, pays online and sees the history; the same account
//     becomes a member after Upgrade to Member (/join);
//   - both confirm a 6-digit code sent by e-mail (WhatsApp when the
//     integration is on) and choose a password; the account is linked to the
//     customer profile and signs in to the same Member App.

import (
	"context"
	"crypto/rand"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/iam"
	"oneclub/internal/platform/notify"
)

const (
	signUpCodeTTL      = 15 * time.Minute
	signUpCodeAttempts = 5
)

// SignUpInput starts a Member App registration.
type SignUpInput struct {
	PropertyID uuid.UUID `json:"propertyId"`
	Kind       string    `json:"kind" enum:"member,guest" doc:"member: an existing member of the club; guest: a non-member account"`
	MemberNo   string    `json:"memberNo,omitempty" doc:"member: the member no. on the card"`
	BirthDate  string    `json:"birthDate,omitempty" doc:"member: YYYY-MM-DD, one of birth date / phone / e-mail must match the club's record"`
	Name       string    `json:"name,omitempty" doc:"guest: full name"`
	Phone      string    `json:"phone,omitempty"`
	Email      string    `json:"email"`
	Channel    string    `json:"channel,omitempty" enum:"email,whatsapp" doc:"Where the code goes (default e-mail)"`
	Website    string    `json:"website,omitempty" doc:"Honeypot — must stay empty"`
}

// SignUp is a started registration waiting for its code.
type SignUp struct {
	ID        uuid.UUID `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	SentTo    string    `json:"sentTo" doc:"Masked e-mail / phone the code was sent to"`
	ExpiresAt time.Time `json:"expiresAt"`
	DemoCode  *string   `json:"demoCode,omitempty" doc:"Only on demo / sandbox instances (e-mail and WhatsApp on hold)"`
}

// SignUpConfirm finishes a registration.
type SignUpConfirm struct {
	Code     string `json:"code"`
	Password string `json:"password"`
}

// SignUpDone is the new account (sign in with the e-mail and password).
type SignUpDone struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Status   string `json:"status" enum:"member,non_member"`
	MemberNo string `json:"memberNo,omitempty"`
}

func signUpCode() string {
	var b strings.Builder
	for range 6 {
		v, _ := rand.Int(rand.Reader, big.NewInt(10))
		b.WriteByte(byte('0' + v.Int64()))
	}
	return b.String()
}

// digits keeps the last 9 digits of a phone number (08…, +628…, 628…).
func phoneTail(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 9 {
		s = s[len(s)-9:]
	}
	return s
}

// StartSignUp checks who registers and sends the code.
func (m *Module) StartSignUp(ctx context.Context, tx pgx.Tx, in SignUpInput) (SignUp, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" || !strings.Contains(email, "@") {
		return SignUp{}, errs.Validation("email_required", "an e-mail address is required for the account", errs.Field("email", "required", "e-mail"))
	}
	var customerID uuid.UUID
	var memberID *uuid.UUID
	var name, phone string
	switch in.Kind {
	case "member":
		no := strings.TrimSpace(in.MemberNo)
		if no == "" {
			return SignUp{}, errs.Validation("member_no_required", "enter the member no. on your card", errs.Field("memberNo", "required", "member no."))
		}
		var mid uuid.UUID
		var cid *uuid.UUID
		var cEmail, cPhone *string
		var birth *time.Time
		var user *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT m.id, m.customer_id, c.name, nullif(c.email, ''), nullif(c.phone, ''), c.birth_date, coalesce(m.user_id, c.user_id)
			FROM membership.members m LEFT JOIN crm.customers c ON c.id = m.customer_id
			WHERE m.property_id = $1 AND upper(m.code) = upper($2) AND m.archived_at IS NULL AND m.status NOT IN ('inactive')`, in.PropertyID, no).
			Scan(&mid, &cid, &name, &cEmail, &cPhone, &birth, &user)
		if dbtx.IsNoRows(err) || (err == nil && cid == nil) {
			return SignUp{}, errs.Validation("member_not_found", "no member with this member no. — check the card or ask the front desk",
				errs.Field("memberNo", "not_found", "member no."))
		}
		if err != nil {
			return SignUp{}, err
		}
		// one detail on file must match: date of birth, phone or e-mail
		ok := (cEmail != nil && strings.EqualFold(*cEmail, email)) ||
			(cPhone != nil && in.Phone != "" && phoneTail(*cPhone) == phoneTail(in.Phone)) ||
			(birth != nil && in.BirthDate != "" && birth.Format("2006-01-02") == in.BirthDate)
		if !ok {
			return SignUp{}, errs.Validation("member_details_mismatch", "the details do not match the club's record — use the date of birth, phone or e-mail you gave the club")
		}
		if cEmail != nil && !strings.EqualFold(*cEmail, email) {
			return SignUp{}, errs.Validation("email_mismatch", "use the e-mail registered with the club ("+mask.Email(*cEmail)+") or ask the front desk to change it",
				errs.Field("email", "mismatch", "registered e-mail"))
		}
		if user != nil {
			var has bool
			if err := tx.QueryRow(ctx, `SELECT password_hash IS NOT NULL FROM platform.users WHERE id = $1`, *user).Scan(&has); err == nil && has {
				return SignUp{}, errs.Conflict("already_registered", "this member already has a Member App account — sign in, or reset the password")
			}
		}
		customerID, memberID = *cid, &mid
		if cPhone != nil {
			phone = *cPhone
		}
	case "guest":
		name = strings.TrimSpace(in.Name)
		if name == "" {
			return SignUp{}, errs.Validation("name_required", "enter your full name", errs.Field("name", "required", "name"))
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.users WHERE email = $1 AND password_hash IS NOT NULL)`, email).Scan(&taken); err != nil {
			return SignUp{}, err
		}
		if taken {
			return SignUp{}, errs.Conflict("already_registered", "an account with this e-mail exists — sign in, or reset the password")
		}
		c, _, err := crm.FindOrCreate(ctx, tx, in.PropertyID, crm.Identity{Name: name, Phone: in.Phone, Email: email})
		if err != nil {
			return SignUp{}, err
		}
		customerID, phone = c.ID, c.Phone
		if c.Name != "" {
			name = c.Name
		}
	default:
		return SignUp{}, errs.Validation("invalid_kind", "kind is member or guest", errs.Field("kind", "invalid", "member or guest"))
	}
	channel := in.Channel
	if channel != "whatsapp" || phone == "" {
		channel = "email"
	}
	code := signUpCode()
	out := SignUp{ID: id.New(), Kind: in.Kind, Name: name, ExpiresAt: clock.Now().Add(signUpCodeTTL), SentTo: mask.Email(email)}
	if channel == "whatsapp" {
		out.SentTo = mask.Phone(phone)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO membership.portal_registrations (id, property_id, kind, customer_id, member_id, email, phone, name, channel, code_hash, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, out.ID, in.PropertyID, in.Kind, customerID, memberID, email, nullStr(phone), name, channel,
		secret.HashToken(out.ID.String()+":"+code), out.ExpiresAt); err != nil {
		return SignUp{}, err
	}
	ch := []string{notify.ChannelEmail}
	if channel == "whatsapp" {
		ch = []string{notify.ChannelWhatsApp}
	}
	if m.Notify != nil {
		if err := m.Notify.Send(ctx, tx, notify.Message{Event: "auth.login_code", Category: "security", Email: email, Phone: phone, Name: name, Mandatory: true,
			Channels: ch, PropertyID: &in.PropertyID, Data: map[string]any{"name": name, "code": code, "expiresInMinutes": int(signUpCodeTTL.Minutes())}}); err != nil {
			return SignUp{}, err
		}
	}
	if m.ShowCodes {
		out.DemoCode = &code
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "portal_signup_started", Category: audit.CategorySecurity,
		EntityType: "membership.portal_registration", EntityID: out.ID.String(), EntityLabel: name, PropertyID: &in.PropertyID, ActorName: name,
		Metadata: map[string]any{"kind": in.Kind, "email": mask.Email(email)}})
}

// ConfirmSignUp checks the code, creates the account with the password and
// links it to the customer (and the member).
func (m *Module) ConfirmSignUp(ctx context.Context, tx pgx.Tx, rid uuid.UUID, in SignUpConfirm) (SignUpDone, error) {
	var property, customer uuid.UUID
	var member *uuid.UUID
	var kind, email, name, hash string
	var phone *string
	var expires time.Time
	var attempts int
	var done *time.Time
	err := tx.QueryRow(ctx, `SELECT property_id, customer_id, member_id, kind, email, phone, name, code_hash, expires_at, attempts, completed_at
		FROM membership.portal_registrations WHERE id = $1 FOR UPDATE`, rid).
		Scan(&property, &customer, &member, &kind, &email, &phone, &name, &hash, &expires, &attempts, &done)
	if dbtx.IsNoRows(err) {
		return SignUpDone{}, errs.NotFound("registration")
	}
	if err != nil {
		return SignUpDone{}, err
	}
	if done != nil {
		return SignUpDone{}, errs.Conflict("already_registered", "the account is already created — sign in")
	}
	if clock.Now().After(expires) || attempts >= signUpCodeAttempts {
		return SignUpDone{}, errs.Validation("code_expired", "the code has expired — start again to get a new code")
	}
	if !secret.Equal(hash, secret.HashToken(rid.String()+":"+strings.TrimSpace(in.Code))) {
		if _, err := tx.Exec(ctx, `UPDATE membership.portal_registrations SET attempts = attempts + 1 WHERE id = $1`, rid); err != nil {
			return SignUpDone{}, err
		}
		return SignUpDone{}, errs.Validation("code_invalid", "the code is not right", errs.Field("code", "invalid", "code"))
	}
	if m.Portal == nil {
		return SignUpDone{}, errs.Conflict("portal_unavailable", "the Member App is not available")
	}
	role := "guest"
	if kind == "member" {
		role = "member"
	}
	uid, _, err := m.Portal.EnsurePortalUser(ctx, tx, iam.PortalUser{Email: email, Name: name, Phone: deref(phone), RoleCode: role, PropertyID: property})
	if err != nil {
		return SignUpDone{}, err
	}
	if err := m.Portal.SetPortalPassword(ctx, tx, uid, email, in.Password); err != nil {
		return SignUpDone{}, err
	}
	if err := crm.LinkUser(ctx, tx, customer, uid); err != nil {
		return SignUpDone{}, err
	}
	// a member without an e-mail on file gets the one of the account
	if _, err := tx.Exec(ctx, `UPDATE crm.customers SET email = $2 WHERE id = $1 AND coalesce(email, '') = ''`, customer, email); err != nil {
		return SignUpDone{}, err
	}
	out := SignUpDone{Email: email, Name: name, Status: "non_member"}
	if member != nil {
		if _, err := tx.Exec(ctx, `UPDATE membership.members SET user_id = $2 WHERE id = $1 AND user_id IS NULL`, *member, uid); err != nil {
			return SignUpDone{}, err
		}
		out.Status = "member"
		_ = tx.QueryRow(ctx, `SELECT code FROM membership.members WHERE id = $1`, *member).Scan(&out.MemberNo)
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.portal_registrations SET completed_at = now(), user_id = $2 WHERE id = $1`, rid, uid); err != nil {
		return SignUpDone{}, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "portal_signup_completed", Category: audit.CategorySecurity,
		EntityType: "platform.user", EntityID: uid.String(), EntityLabel: name, PropertyID: &property, ActorName: name,
		Metadata: map[string]any{"kind": kind, "email": mask.Email(email)}})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (m *Module) registerSignUp(reg *route.Registry) {
	db := m.DB
	crm.PublicRoute(reg, "membership", route.Route{Method: http.MethodPost, Path: "/api/v1/public/portal-registrations",
		Summary: "Member App sign-up: a member (member no. + a detail on file) or a non-member guest; sends a one-time code",
		Request: SignUpInput{}, Response: SignUp{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if !crm.PublicLimiter.Allow(clientIP(r) + r.URL.Path) {
				httpx.WriteError(w, r, errs.RateLimited())
				return
			}
			var in SignUpInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if in.Website != "" {
				httpx.JSON(w, http.StatusAccepted, map[string]any{"status": "received"})
				return
			}
			if in.PropertyID == uuid.Nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), in.PropertyID)
			var out SignUp
			err := db.WithTx(ctx, func(tx pgx.Tx) error {
				var err error
				out, err = m.StartSignUp(ctx, tx, in)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusCreated, out)
		}})
	crm.PublicRoute(reg, "membership", route.Route{Method: http.MethodPost, Path: "/api/v1/public/portal-registrations/{id}:confirm",
		Summary: "Member App sign-up: confirm the code and choose the password", Request: SignUpConfirm{}, Response: SignUpDone{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if !crm.PublicLimiter.Allow(clientIP(r) + r.URL.Path) {
				httpx.WriteError(w, r, errs.RateLimited())
				return
			}
			rid, err := uuid.Parse(chi.URLParam(r, "id"))
			if err != nil {
				httpx.WriteError(w, r, errs.NotFound("registration"))
				return
			}
			var in SignUpConfirm
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			// the registration carries its property
			var pid uuid.UUID
			sys := dbtx.System(r.Context())
			if err := db.WithReadTx(sys, func(tx pgx.Tx) error {
				return tx.QueryRow(sys, `SELECT property_id FROM membership.portal_registrations WHERE id = $1`, rid).Scan(&pid)
			}); err != nil {
				httpx.WriteError(w, r, errs.NotFound("registration"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out SignUpDone
			err = db.WithTx(ctx, func(tx pgx.Tx) error {
				var err error
				out, err = m.ConfirmSignUp(ctx, tx, rid, in)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
}

func clientIP(r *http.Request) string {
	if m := reqctx.GetMeta(r.Context()); m != nil && m.IP != "" {
		return m.IP
	}
	return r.RemoteAddr
}
