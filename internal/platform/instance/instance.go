// Package instance manages the customer instance (EP-01, EP-11):
// configuration, Enabled Modules, Feature Flags, Custom Domains, Branding
// and Localization, plus the public bootstrap used by every shell.
package instance

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/audit"
)

// Service implements httpapi.Gate and the instance endpoints.
type Service struct {
	DB *dbtx.DB

	tzMu    sync.RWMutex
	tz      *time.Location
	tzUntil time.Time
}

// ModuleEnabled reports whether a module is enabled (FR-INS-04).
func (s *Service) ModuleEnabled(ctx context.Context, module string) (bool, error) {
	var on bool
	err := s.DB.Primary.QueryRow(ctx, `SELECT enabled FROM platform.modules WHERE code = $1`, module).Scan(&on)
	if dbtx.IsNoRows(err) {
		return false, nil
	}
	return on, err
}

// Suspended reports whether the instance is suspended (FR-INS-03).
func (s *Service) Suspended(ctx context.Context) (bool, error) {
	var st string
	err := s.DB.Primary.QueryRow(ctx, `SELECT status FROM platform.instance`).Scan(&st)
	if dbtx.IsNoRows(err) {
		return false, nil
	}
	return st == "suspended", err
}

// Location returns the instance timezone (cached for one minute) for
// periodic jobs (FR-JOB-03).
func (s *Service) Location() *time.Location {
	s.tzMu.RLock()
	if s.tz != nil && time.Now().Before(s.tzUntil) {
		defer s.tzMu.RUnlock()
		return s.tz
	}
	s.tzMu.RUnlock()
	var name string
	if err := s.DB.Primary.QueryRow(context.Background(), `SELECT timezone FROM platform.instance`).Scan(&name); err != nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	s.tzMu.Lock()
	s.tz, s.tzUntil = loc, time.Now().Add(time.Minute)
	s.tzMu.Unlock()
	return loc
}

// ── DTOs ──────────────────────────────────────────────────────────────────

type Branding struct {
	AppName         string            `json:"appName"`
	EmailSenderName string            `json:"emailSenderName"`
	LogoURL         *string           `json:"logoUrl"`
	FaviconURL      *string           `json:"faviconUrl"`
	LoginImageURL   *string           `json:"loginImageUrl"`
	PrimaryColor    *string           `json:"primaryColor"`
	Accent          string            `json:"accent" enum:"lime,blue,violet,orange,rose,custom"`
	CustomAccent    *AccentTokens     `json:"customAccent"`
	Extra           map[string]string `json:"extra,omitempty"`
}

type Instance struct {
	ID            uuid.UUID `json:"id"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	DefaultLocale string    `json:"defaultLocale" enum:"en,id"`
	Currency      string    `json:"currency"`
	Timezone      string    `json:"timezone"`
	Status        string    `json:"status" enum:"active,suspended"`
	DefaultTheme  string    `json:"defaultTheme" enum:"light,dark"`
	Branding      Branding  `json:"branding"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type InstanceUpdate struct {
	Name          *string `json:"name,omitempty"`
	DefaultLocale *string `json:"defaultLocale,omitempty" enum:"en,id"`
	Currency      *string `json:"currency,omitempty"`
	Timezone      *string `json:"timezone,omitempty"`
	Status        *string `json:"status,omitempty" enum:"active,suspended"`
	DefaultTheme  *string `json:"defaultTheme,omitempty" enum:"light,dark"`
}

type LocalizationUpdate struct {
	DefaultLocale *string `json:"defaultLocale,omitempty" enum:"en,id"`
	Currency      *string `json:"currency,omitempty"`
	Timezone      *string `json:"timezone,omitempty"`
	DefaultTheme  *string `json:"defaultTheme,omitempty" enum:"light,dark"`
}

type BrandingUpdate struct {
	AppName         *string `json:"appName,omitempty"`
	EmailSenderName *string `json:"emailSenderName,omitempty"`
	LogoURL         *string `json:"logoUrl,omitempty"`
	FaviconURL      *string `json:"faviconUrl,omitempty"`
	LoginImageURL   *string `json:"loginImageUrl,omitempty"`
	PrimaryColor    *string `json:"primaryColor,omitempty" doc:"#RRGGBB brand colour"`
	Accent          *string `json:"accent,omitempty" enum:"lime,blue,violet,orange,rose,custom"`
}

type Module struct {
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Layer     string    `json:"layer"`
	SortOrder int       `json:"sortOrder"`
	Enabled   bool      `json:"enabled"`
	AlwaysOn  bool      `json:"alwaysOn"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ModulesUpdate struct {
	Modules []struct {
		Code    string `json:"code"`
		Enabled bool   `json:"enabled"`
	} `json:"modules"`
}

type FeatureFlag struct {
	Key           string          `json:"key"`
	Description   string          `json:"description"`
	Value         json.RawMessage `json:"value"`
	ValueType     string          `json:"valueType" enum:"boolean,string,number,json"`
	ClientVisible bool            `json:"clientVisible"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

type FeatureFlagUpdate struct {
	Value         json.RawMessage `json:"value"`
	Description   *string         `json:"description,omitempty"`
	ValueType     *string         `json:"valueType,omitempty" enum:"boolean,string,number,json"`
	ClientVisible *bool           `json:"clientVisible,omitempty"`
}

type Domain struct {
	ID                uuid.UUID  `json:"id"`
	Surface           string     `json:"surface" enum:"web,member,dashboard,cashier,caddy,kitchen,api,backoffice,ops,platform-admin" doc:"Staff App: dashboard, cashier, caddy or kitchen; backoffice, platform-admin (served as dashboard) and ops (as cashier) are the former staff surfaces"`
	Hostname          string     `json:"hostname"`
	Status            string     `json:"status" enum:"pending,verified,active,failed"`
	VerificationName  string     `json:"verificationRecordName" doc:"DNS TXT record name to create"`
	VerificationValue string     `json:"verificationRecordValue"`
	VerifiedAt        *time.Time `json:"verifiedAt"`
	LastCheckedAt     *time.Time `json:"lastCheckedAt"`
	LastError         *string    `json:"lastError"`
	CreatedAt         time.Time  `json:"createdAt"`
}

type DomainRequest struct {
	Surface  string `json:"surface" enum:"web,member,dashboard,cashier,caddy,kitchen,api,backoffice,ops,platform-admin" doc:"Staff App: dashboard, cashier, caddy or kitchen; backoffice, platform-admin (served as dashboard) and ops (as cashier) are the former staff surfaces"`
	Hostname string `json:"hostname"`
}

type Bootstrap struct {
	Code           string            `json:"code"`
	Name           string            `json:"name"`
	DefaultLocale  string            `json:"defaultLocale"`
	Currency       string            `json:"currency"`
	Timezone       string            `json:"timezone"`
	DefaultTheme   string            `json:"defaultTheme"`
	Status         string            `json:"status"`
	Branding       Branding          `json:"branding"`
	EnabledModules []string          `json:"enabledModules"`
	Flags          map[string]any    `json:"flags"`
	Labels         map[string]string `json:"moduleLabels"`
	Properties     []PublicProperty  `json:"properties" doc:"Active properties (website booking, public endpoints)"`
}

// PublicProperty is a property shown on the public website.
type PublicProperty struct {
	ID   uuid.UUID `json:"id"`
	Code string    `json:"code"`
	Name string    `json:"name"`
}

// ── loading ───────────────────────────────────────────────────────────────

func decodeBranding(raw map[string]any, name string) Branding {
	b := Branding{AppName: name, EmailSenderName: name, Accent: "lime"}
	str := func(k string) *string {
		if v, ok := raw[k].(string); ok && v != "" {
			return &v
		}
		return nil
	}
	if v := str("appName"); v != nil {
		b.AppName = *v
	}
	if v := str("emailSenderName"); v != nil {
		b.EmailSenderName = *v
	}
	b.LogoURL, b.FaviconURL, b.LoginImageURL, b.PrimaryColor = str("logoUrl"), str("faviconUrl"), str("loginImageUrl"), str("primaryColor")
	if v := str("accent"); v != nil {
		b.Accent = *v
	}
	if b.Accent == "custom" && b.PrimaryColor != nil {
		if tok, err := GenerateAccent(*b.PrimaryColor); err == nil {
			b.CustomAccent = &tok
		}
	}
	return b
}

func (s *Service) load(ctx context.Context, q dbtx.Querier, lock bool) (Instance, map[string]any, error) {
	var in Instance
	var raw map[string]any
	sql := `SELECT id, code, name, default_locale, currency, timezone, status, default_theme, branding, updated_at FROM platform.instance`
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql).Scan(&in.ID, &in.Code, &in.Name, &in.DefaultLocale, &in.Currency, &in.Timezone, &in.Status, &in.DefaultTheme, &raw, &in.UpdatedAt)
	if dbtx.IsNoRows(err) {
		return in, nil, errs.NotFound("instance")
	}
	if raw == nil {
		raw = map[string]any{}
	}
	in.Branding = decodeBranding(raw, in.Name)
	return in, raw, err
}

func (s *Service) getInstance(w http.ResponseWriter, r *http.Request) {
	in, _, err := s.load(r.Context(), s.DB.Primary, false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, in)
}

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

func validateLocalization(locale, currency, tz, theme *string) error {
	var fields []errs.FieldError
	if locale != nil && *locale != "en" && *locale != "id" {
		fields = append(fields, errs.Field("defaultLocale", "invalid", "en or id"))
	}
	if currency != nil && !currencyRe.MatchString(*currency) {
		fields = append(fields, errs.Field("currency", "invalid", "ISO 4217 code, e.g. IDR"))
	}
	if tz != nil {
		if _, err := time.LoadLocation(*tz); err != nil || *tz == "" {
			fields = append(fields, errs.Field("timezone", "invalid", "IANA timezone, e.g. Asia/Jakarta"))
		}
	}
	if theme != nil && *theme != "light" && *theme != "dark" {
		fields = append(fields, errs.Field("defaultTheme", "invalid", "light or dark"))
	}
	if len(fields) > 0 {
		return errs.Validation("invalid_localization", "invalid localization settings", fields...)
	}
	return nil
}

func (s *Service) updateInstance(localization bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req InstanceUpdate
		if localization {
			var lr LocalizationUpdate
			if err := httpx.Decode(r, &lr); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			req = InstanceUpdate{DefaultLocale: lr.DefaultLocale, Currency: lr.Currency, Timezone: lr.Timezone, DefaultTheme: lr.DefaultTheme}
		} else if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if err := validateLocalization(req.DefaultLocale, req.Currency, req.Timezone, req.DefaultTheme); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if req.Status != nil && *req.Status != "active" && *req.Status != "suspended" {
			httpx.WriteError(w, r, errs.Validation("invalid_status", "invalid status", errs.Field("status", "invalid", "active or suspended")))
			return
		}
		if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
			httpx.WriteError(w, r, errs.Validation("invalid_name", "name is required", errs.Field("name", "required", "name is required")))
			return
		}
		ctx := r.Context()
		var out Instance
		err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			before, _, err := s.load(ctx, tx, true)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE platform.instance SET name = coalesce($1, name), default_locale = coalesce($2, default_locale),
				currency = coalesce($3, currency), timezone = coalesce($4, timezone), status = coalesce($5, status),
				default_theme = coalesce($6, default_theme), updated_at = now(), updated_by = $7`,
				req.Name, req.DefaultLocale, req.Currency, req.Timezone, req.Status, req.DefaultTheme, id.Ptr(authz.From(ctx).UserID)); err != nil {
				return err
			}
			out, _, err = s.load(ctx, tx, false)
			if err != nil {
				return err
			}
			action := audit.ActionUpdate
			if before.Status != out.Status {
				action = audit.ActionStatusChange
			}
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: action, EntityType: "platform.instance",
				EntityID: out.Code, EntityLabel: out.Name, Before: before, After: out})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		s.tzMu.Lock()
		s.tz = nil
		s.tzMu.Unlock()
		httpx.JSON(w, http.StatusOK, out)
	}
}

// ── Branding (FR-BRD-01..04, FR-INS-07) ──────────────────────────────────

func (s *Service) getBranding(w http.ResponseWriter, r *http.Request) {
	in, _, err := s.load(r.Context(), s.DB.Primary, false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, in.Branding)
}

func validURL(s string) bool {
	return strings.HasPrefix(s, "/api/v1/files/") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

func (s *Service) updateBranding(w http.ResponseWriter, r *http.Request) {
	var req BrandingUpdate
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out Branding
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, raw, err := s.load(ctx, tx, true)
		if err != nil {
			return err
		}
		var fields []errs.FieldError
		set := func(key string, v *string, check func(string) bool, msg string) {
			if v == nil {
				return
			}
			if *v == "" {
				delete(raw, key)
				return
			}
			if check != nil && !check(*v) {
				fields = append(fields, errs.Field(key, "invalid", msg))
				return
			}
			raw[key] = *v
		}
		set("appName", req.AppName, func(v string) bool { return len(v) <= 60 }, "at most 60 characters")
		set("emailSenderName", req.EmailSenderName, func(v string) bool { return len(v) <= 60 }, "at most 60 characters")
		set("logoUrl", req.LogoURL, validURL, "upload an image or use an https URL")
		set("faviconUrl", req.FaviconURL, validURL, "upload an image or use an https URL")
		set("loginImageUrl", req.LoginImageURL, validURL, "upload an image or use an https URL")
		set("primaryColor", req.PrimaryColor, func(v string) bool { return hexRe.MatchString(v) }, "#RRGGBB colour")
		if req.Accent != nil {
			if *req.Accent != "custom" && !slices.Contains(Presets, *req.Accent) {
				fields = append(fields, errs.Field("accent", "invalid", "lime, blue, violet, orange, rose or custom"))
			} else {
				raw["accent"] = *req.Accent
			}
		}
		if len(fields) > 0 {
			return errs.Validation("invalid_branding", "invalid branding", fields...)
		}
		if raw["accent"] == "custom" {
			pc, _ := raw["primaryColor"].(string)
			if pc == "" {
				return errs.Validation("primary_color_required", "a custom accent needs a primary colour", errs.Field("primaryColor", "required", "set the brand colour"))
			}
			if _, err := GenerateAccent(pc); err != nil {
				return errs.Validation("contrast_failed", err.Error(), errs.Field("primaryColor", "contrast", err.Error()))
			}
		}
		rawJSON, _ := json.Marshal(raw)
		if _, err := tx.Exec(ctx, `UPDATE platform.instance SET branding = $1::jsonb, updated_at = now(), updated_by = $2`, rawJSON, id.Ptr(authz.From(ctx).UserID)); err != nil {
			return err
		}
		after, _, err := s.load(ctx, tx, false)
		if err != nil {
			return err
		}
		out = after.Branding
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionUpdate, EntityType: "platform.branding",
			EntityID: after.Code, EntityLabel: out.AppName, Before: before.Branding, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// AccentPreviewRequest previews a custom accent.
type AccentPreviewRequest struct {
	PrimaryColor string `json:"primaryColor"`
}

func (s *Service) previewAccent(w http.ResponseWriter, r *http.Request) {
	var req AccentPreviewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	tok, err := GenerateAccent(req.PrimaryColor)
	if err != nil && tok.Light == nil {
		httpx.WriteError(w, r, errs.Validation("invalid_color", err.Error(), errs.Field("primaryColor", "invalid", err.Error())))
		return
	}
	httpx.JSON(w, http.StatusOK, tok)
}

// ── Modules (FR-INS-04) ───────────────────────────────────────────────────

func (s *Service) listModules(ctx context.Context, q dbtx.Querier) ([]Module, error) {
	rows, err := q.Query(ctx, `SELECT code, name, layer, sort_order, enabled, always_on, updated_at FROM platform.modules ORDER BY sort_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Module{}
	for rows.Next() {
		var m Module
		if err := rows.Scan(&m.Code, &m.Name, &m.Layer, &m.SortOrder, &m.Enabled, &m.AlwaysOn, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) getModules(w http.ResponseWriter, r *http.Request) {
	out, err := s.listModules(r.Context(), s.DB.Primary)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Module]{Items: out})
}

func (s *Service) putModules(w http.ResponseWriter, r *http.Request) {
	var req ModulesUpdate
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out []Module
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := s.listModules(ctx, tx)
		if err != nil {
			return err
		}
		idx := map[string]Module{}
		for _, m := range before {
			idx[m.Code] = m
		}
		for _, m := range req.Modules {
			cur, ok := idx[m.Code]
			if !ok {
				return errs.Validation("unknown_module", "unknown module", errs.Field("modules", "unknown", m.Code))
			}
			if cur.AlwaysOn && !m.Enabled {
				return errs.Validation("module_required", cur.Name+" cannot be disabled", errs.Field("modules", "always_on", m.Code))
			}
			if cur.Enabled == m.Enabled {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE platform.modules SET enabled = $2, updated_at = now(), updated_by = $3 WHERE code = $1`,
				m.Code, m.Enabled, id.Ptr(authz.From(ctx).UserID)); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionStatusChange, EntityType: "platform.module",
				EntityID: m.Code, EntityLabel: cur.Name, Before: map[string]any{"enabled": cur.Enabled}, After: map[string]any{"enabled": m.Enabled}}); err != nil {
				return err
			}
		}
		out, err = s.listModules(ctx, tx)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Module]{Items: out})
}

// ── Feature flags (FR-INS-05) ─────────────────────────────────────────────

func (s *Service) flags(ctx context.Context, q dbtx.Querier, clientOnly bool) ([]FeatureFlag, error) {
	where := "true"
	if clientOnly {
		where = "client_visible"
	}
	rows, err := q.Query(ctx, `SELECT key, description, value, value_type, client_visible, updated_at FROM platform.feature_flags WHERE `+where+` ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeatureFlag{}
	for rows.Next() {
		var f FeatureFlag
		var raw []byte
		if err := rows.Scan(&f.Key, &f.Description, &raw, &f.ValueType, &f.ClientVisible, &f.UpdatedAt); err != nil {
			return nil, err
		}
		f.Value = raw
		out = append(out, f)
	}
	return out, rows.Err()
}

// Flag reads a flag value for backend code.
func (s *Service) Flag(ctx context.Context, q dbtx.Querier, key string) (json.RawMessage, bool) {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT value FROM platform.feature_flags WHERE key = $1`, key).Scan(&raw); err != nil {
		return nil, false
	}
	return raw, true
}

// FlagBool reads a boolean flag (false when missing).
func (s *Service) FlagBool(ctx context.Context, q dbtx.Querier, key string) bool {
	raw, ok := s.Flag(ctx, q, key)
	if !ok {
		return false
	}
	var b bool
	_ = json.Unmarshal(raw, &b)
	return b
}

func (s *Service) getFlags(w http.ResponseWriter, r *http.Request) {
	out, err := s.flags(r.Context(), s.DB.Primary, false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[FeatureFlag]{Items: out})
}

var flagKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_.]{1,80}$`)

func checkFlagValue(t string, v json.RawMessage) bool {
	var x any
	if json.Unmarshal(v, &x) != nil {
		return false
	}
	switch t {
	case "boolean":
		_, ok := x.(bool)
		return ok
	case "string":
		_, ok := x.(string)
		return ok
	case "number":
		_, ok := x.(float64)
		return ok
	}
	return true
}

func (s *Service) putFlag(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if !flagKeyRe.MatchString(key) {
		httpx.WriteError(w, r, errs.Validation("invalid_key", "invalid flag key", errs.Field("key", "invalid", "lower-case letters, digits, dot and underscore")))
		return
	}
	var req FeatureFlagUpdate
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out FeatureFlag
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var before *FeatureFlag
		all, err := s.flags(ctx, tx, false)
		if err != nil {
			return err
		}
		for _, f := range all {
			if f.Key == key {
				f := f
				before = &f
			}
		}
		vt := "boolean"
		if before != nil {
			vt = before.ValueType
		}
		if req.ValueType != nil {
			vt = *req.ValueType
		}
		if !checkFlagValue(vt, req.Value) {
			return errs.Validation("invalid_value", "value does not match the flag type", errs.Field("value", "invalid", "expected "+vt))
		}
		desc := ""
		if req.Description != nil {
			desc = *req.Description
		} else if before != nil {
			desc = before.Description
		}
		cv := true
		if req.ClientVisible != nil {
			cv = *req.ClientVisible
		} else if before != nil {
			cv = before.ClientVisible
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.feature_flags (key, description, value, value_type, client_visible, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (key) DO UPDATE SET description = EXCLUDED.description, value = EXCLUDED.value,
			value_type = EXCLUDED.value_type, client_visible = EXCLUDED.client_visible, updated_at = now(), updated_by = EXCLUDED.updated_by`,
			key, desc, []byte(req.Value), vt, cv, id.Ptr(authz.From(ctx).UserID)); err != nil {
			return err
		}
		all, err = s.flags(ctx, tx, false)
		if err != nil {
			return err
		}
		for _, f := range all {
			if f.Key == key {
				out = f
			}
		}
		action := audit.ActionUpdate
		if before == nil {
			action = audit.ActionCreate
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: action, EntityType: "platform.feature_flag",
			EntityID: key, EntityLabel: key, Before: before, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ── Custom domains (FR-INS-06) ────────────────────────────────────────────

var hostRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

const verifyPrefix = "_oneclub-verify."

func scanDomain(row pgx.Row) (Domain, error) {
	var d Domain
	var token string
	err := row.Scan(&d.ID, &d.Surface, &d.Hostname, &d.Status, &token, &d.VerifiedAt, &d.LastCheckedAt, &d.LastError, &d.CreatedAt)
	d.VerificationName = verifyPrefix + d.Hostname
	d.VerificationValue = "oneclub-verification=" + token
	return d, err
}

const domainSelect = `SELECT id, surface, hostname::text, status, verification_token, verified_at, last_checked_at, last_error, created_at FROM platform.domains`

func (s *Service) listDomains(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.DB.Primary.Query(ctx, domainSelect+` ORDER BY surface, hostname`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, d)
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Domain]{Items: out})
}

func (s *Service) addDomain(w http.ResponseWriter, r *http.Request) {
	var req DomainRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req.Hostname = strings.ToLower(strings.TrimSpace(req.Hostname))
	var fields []errs.FieldError
	if !hostRe.MatchString(req.Hostname) {
		fields = append(fields, errs.Field("hostname", "invalid", "e.g. booking.modernclub.com"))
	}
	if !slices.Contains(domainSurfaces, req.Surface) {
		fields = append(fields, errs.Field("surface", "invalid", "unknown application surface"))
	}
	if len(fields) > 0 {
		httpx.WriteError(w, r, errs.Validation("invalid_domain", "invalid domain", fields...))
		return
	}
	ctx := r.Context()
	var out Domain
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		did := id.New()
		uid := id.Ptr(authz.From(ctx).UserID)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domains (id, surface, hostname, verification_token, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,$5)`, did, req.Surface, req.Hostname, secret.RandomToken(18), uid); err != nil {
			if ok, _ := dbtx.IsUniqueViolation(err); ok {
				return errs.Validation("hostname_taken", "hostname already registered", errs.Field("hostname", "taken", "already registered"))
			}
			return err
		}
		var err error
		if out, err = scanDomain(tx.QueryRow(ctx, domainSelect+` WHERE id = $1`, did)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, EntityType: "platform.domain",
			EntityID: did.String(), EntityLabel: req.Hostname, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// Resolver looks up TXT records (replaceable in tests).
var Resolver = func(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, name)
}

// verifyDomain checks the TXT record; once verified the domain is Active and
// Caddy issues the certificate on demand (it asks /api/v1/public/domains/allowed).
func (s *Service) verifyDomain(w http.ResponseWriter, r *http.Request) {
	did, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out Domain
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := scanDomain(tx.QueryRow(ctx, domainSelect+` WHERE id = $1 FOR UPDATE`, did))
		if dbtx.IsNoRows(err) {
			return errs.NotFound("domain")
		}
		if err != nil {
			return err
		}
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		txts, lerr := Resolver(lctx, before.VerificationName)
		cancel()
		ok := lerr == nil && slices.Contains(txts, before.VerificationValue)
		status, msg := "active", (*string)(nil)
		if !ok {
			status = "failed"
			m := "TXT record " + before.VerificationName + " not found or does not match"
			if lerr != nil {
				m += ": " + lerr.Error()
			}
			msg = &m
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.domains SET status = $2, last_checked_at = now(), last_error = $3,
			verified_at = CASE WHEN $2 = 'active' THEN coalesce(verified_at, now()) ELSE verified_at END, updated_at = now() WHERE id = $1`,
			did, status, msg); err != nil {
			return err
		}
		if out, err = scanDomain(tx.QueryRow(ctx, domainSelect+` WHERE id = $1`, did)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "domain_verify", EntityType: "platform.domain",
			EntityID: did.String(), EntityLabel: out.Hostname, Before: map[string]any{"status": before.Status}, After: map[string]any{"status": out.Status}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Service) deleteDomain(w http.ResponseWriter, r *http.Request) {
	did, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := scanDomain(tx.QueryRow(ctx, domainSelect+` WHERE id = $1 FOR UPDATE`, did))
		if dbtx.IsNoRows(err) {
			return errs.NotFound("domain")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.domains WHERE id = $1`, did); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionDelete, EntityType: "platform.domain",
			EntityID: did.String(), EntityLabel: before.Hostname, Before: before})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// domainSurfaces are the application surfaces a custom domain can serve.
// The Staff App is one build on four surfaces (Technical Doc §6.1); the
// surfaces of the former staff apps stay valid for domains registered
// before (expand-only).
var domainSurfaces = []string{"web", "member", "dashboard", "cashier", "caddy", "kitchen", "api", "backoffice", "ops", "platform-admin"}

// servedSurface is the surface Caddy serves for a domain's surface.
func servedSurface(surface string) string {
	switch surface {
	case "backoffice", "platform-admin":
		return "dashboard"
	case "ops":
		return "cashier"
	}
	return surface
}

// domainAllowed answers Caddy's on-demand TLS "ask" request. The X-Surface
// header tells Caddy which application to serve on the custom domain (and
// the Staff App its surface, through /surface.json).
func (s *Service) domainAllowed(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.URL.Query().Get("domain"))
	var surface string
	err := s.DB.Primary.QueryRow(r.Context(), `SELECT surface FROM platform.domains WHERE hostname = $1 AND status = 'active'`, host).Scan(&surface)
	if dbtx.IsNoRows(err) {
		err = errs.NotFound("domain")
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	surface = servedSurface(surface)
	w.Header().Set("X-Surface", surface)
	httpx.JSON(w, http.StatusOK, map[string]string{"domain": host, "surface": surface})
}

// ── Bootstrap ─────────────────────────────────────────────────────────────

func (s *Service) bootstrap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	in, _, err := s.load(ctx, s.DB.Primary, false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	mods, err := s.listModules(ctx, s.DB.Primary)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	flags, err := s.flags(ctx, s.DB.Primary, true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	b := Bootstrap{Code: in.Code, Name: in.Name, DefaultLocale: in.DefaultLocale, Currency: in.Currency, Timezone: in.Timezone,
		DefaultTheme: in.DefaultTheme, Status: in.Status, Branding: in.Branding, EnabledModules: []string{}, Flags: map[string]any{},
		Labels: map[string]string{}}
	for _, m := range mods {
		b.Labels[m.Code] = m.Name
		if m.Enabled {
			b.EnabledModules = append(b.EnabledModules, m.Code)
		}
	}
	for _, f := range flags {
		var v any
		_ = json.Unmarshal(f.Value, &v)
		b.Flags[f.Key] = v
	}
	b.Properties = []PublicProperty{}
	if err := s.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, code, name FROM platform.properties WHERE status = 'active' AND archived_at IS NULL ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p PublicProperty
			if err := rows.Scan(&p.ID, &p.Code, &p.Name); err != nil {
				return err
			}
			b.Properties = append(b.Properties, p)
		}
		return rows.Err()
	}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	httpx.JSON(w, http.StatusOK, b)
}

// Register adds the instance routes (PRD §10 Instance).
func (s *Service) Register(reg *route.Registry) {
	add := func(tag string, rt route.Route) { rt.Module = "platform"; rt.Tag = tag; reg.Add(rt) }
	const ci, br = "Customer Instance", "Branding"
	add(ci, route.Route{Method: http.MethodGet, Path: "/api/v1/public/bootstrap", Summary: "Instance bootstrap for all shells (public)",
		Auth: route.AuthPublic, Response: Bootstrap{}, Handler: s.bootstrap})
	add(ci, route.Route{Method: http.MethodGet, Path: "/api/v1/public/domains/allowed", Summary: "Caddy on-demand TLS check and the surface served on a custom domain",
		Auth: route.AuthPublic, Query: []route.Param{{Name: "domain", Required: true}}, Response: map[string]string{}, Handler: s.domainAllowed})
	add(ci, route.Route{Method: http.MethodGet, Path: "/api/v1/platform/instance", Summary: "Instance configuration",
		Permission: "platform.instance.view", Response: Instance{}, Handler: s.getInstance})
	add(ci, route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/instance", Summary: "Edit instance configuration (Platform Admin)",
		Permission: "platform.instance.update", Request: InstanceUpdate{}, Response: Instance{}, Handler: s.updateInstance(false)})
	add(ci, route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/localization", Summary: "Edit locale, currency and timezone",
		Permission: "platform.localization.update", Request: LocalizationUpdate{}, Response: Instance{}, Handler: s.updateInstance(true)})
	add(ci, route.Route{Method: http.MethodGet, Path: "/api/v1/platform/modules", Summary: "Enabled Modules",
		Permission: "platform.module.view", Response: Module{}, List: true, Handler: s.getModules})
	add(ci, route.Route{Method: http.MethodPut, Path: "/api/v1/platform/modules", Summary: "Enable or disable modules (Platform Admin)",
		Permission: "platform.module.update", Request: ModulesUpdate{}, Response: Module{}, List: true, Handler: s.putModules})
	add(ci, route.Route{Method: http.MethodGet, Path: "/api/v1/platform/feature-flags", Summary: "Feature flags",
		Permission: "platform.feature_flag.view", Response: FeatureFlag{}, List: true, Handler: s.getFlags})
	add(ci, route.Route{Method: http.MethodPut, Path: "/api/v1/platform/feature-flags/{key}", Summary: "Set feature flag (Platform Admin)",
		Permission: "platform.feature_flag.update", Request: FeatureFlagUpdate{}, Response: FeatureFlag{}, Handler: s.putFlag})
	add(ci, route.Route{Method: http.MethodGet, Path: "/api/v1/platform/domains", Summary: "Custom domains",
		Permission: "platform.domain.view", Response: Domain{}, List: true, Handler: s.listDomains})
	add(ci, route.Route{Method: http.MethodPost, Path: "/api/v1/platform/domains", Summary: "Register custom domain",
		Permission: "platform.domain.manage", Request: DomainRequest{}, Response: Domain{}, Handler: s.addDomain})
	add(ci, route.Route{Method: http.MethodPost, Path: "/api/v1/platform/domains/{id}:verify", Summary: "Verify DNS and activate (TLS issued automatically)",
		Permission: "platform.domain.manage", Response: Domain{}, Status: http.StatusOK, Handler: s.verifyDomain})
	add(ci, route.Route{Method: http.MethodDelete, Path: "/api/v1/platform/domains/{id}", Summary: "Remove custom domain",
		Permission: "platform.domain.manage", Handler: s.deleteDomain})
	add(br, route.Route{Method: http.MethodGet, Path: "/api/v1/public/branding", Summary: "Branding (public, used by login pages)",
		Auth: route.AuthPublic, Response: Branding{}, Handler: s.getBranding})
	add(br, route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/branding", Summary: "Edit branding",
		Permission: "platform.branding.update", Request: BrandingUpdate{}, Response: Branding{}, Handler: s.updateBranding})
	add(br, route.Route{Method: http.MethodPost, Path: "/api/v1/platform/branding/accent-preview", Summary: "Preview a custom accent with WCAG AA checks",
		Permission: "platform.branding.update", Request: AccentPreviewRequest{}, Response: AccentTokens{}, Status: http.StatusOK,
		NoAudit: "read-only computation", Handler: s.previewAccent})
}
