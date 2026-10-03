// Package httpapi mounts the route registry onto an HTTP server and applies
// the per-route pipeline:
//
//	authenticate → CSRF origin check → instance status & module gate
//	→ property scope + permission (authz) → RLS scope → idempotency
//	→ handler → audit enforcement
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
)

// Authenticator resolves the principal of a request; (nil, nil) means no
// credentials were presented.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (*authz.Principal, error)
}

// Gate reports instance status and module enablement (FR-INS-03/04).
type Gate interface {
	ModuleEnabled(ctx context.Context, module string) (enabled bool, err error)
	Suspended(ctx context.Context) (bool, error)
}

// Server is the API HTTP server.
type Server struct {
	Cfg      *config.Config
	DB       *dbtx.DB
	Registry *route.Registry
	Auth     Authenticator
	Gate     Gate
	OpenAPI  []byte

	mu         sync.Mutex
	violations []string
	exercised  map[string]bool
}

// AuditViolations returns routes that answered 2xx to a mutation without
// writing an audit entry (CI fails when non-empty; EP-07 AC).
func (s *Server) AuditViolations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.violations...)
}

// ExercisedMutations returns mutating routes that returned 2xx at least once.
func (s *Server) ExercisedMutations() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for k, v := range s.exercised {
		out[k] = v
	}
	return out
}

// Handler builds the root handler.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.recoverer, s.requestMeta, s.securityHeaders, s.cors, s.accessLog)
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteError(w, req, errs.NotFound("route"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteError(w, req, errs.BadRequest("method_not_allowed", "method not allowed"))
	})
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Cfg.Version})
	})
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := s.DB.Primary.Ping(ctx); err != nil {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "database": "down"})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	if s.OpenAPI != nil {
		r.Get("/api/v1/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(s.OpenAPI)
		})
	}
	for _, rt := range s.Registry.Routes() {
		r.Method(rt.Method, rt.Path, s.wrap(rt))
	}
	return r
}

type statusWriter struct {
	http.ResponseWriter
	status int
	body   *strings.Builder // captured only when idempotency needs it
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.body != nil {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer (SSE
// streams clear the write deadline and flush through it).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				slog.ErrorContext(r.Context(), "panic", "panic", p, "stack", string(debug.Stack()))
				httpx.WriteError(w, r, errs.Internal(errors.New("panic")))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-Id")
		if rid == "" || len(rid) > 64 {
			rid = id.New().String()
		}
		w.Header().Set("X-Request-Id", rid)
		m := &reqctx.Meta{
			RequestID: rid,
			IP:        httpx.ClientIP(r),
			UserAgent: r.UserAgent(),
			Locale:    negotiateLocale(r),
		}
		next.ServeHTTP(w, r.WithContext(reqctx.WithMeta(r.Context(), m)))
	})
}

// negotiateLocale picks "id" or "en" from X-Locale or Accept-Language.
func negotiateLocale(r *http.Request) string {
	if l := r.Header.Get("X-Locale"); l == "id" || l == "en" {
		return l
	}
	al := strings.ToLower(r.Header.Get("Accept-Language"))
	for _, part := range strings.Split(al, ",") {
		tag := strings.TrimSpace(strings.Split(part, ";")[0])
		if strings.HasPrefix(tag, "id") || strings.HasPrefix(tag, "in") {
			return "id"
		}
		if strings.HasPrefix(tag, "en") {
			return "en"
		}
	}
	return ""
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cache-Control", "no-store")
		if s.Cfg.CookieSecure {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(s.Cfg.AllowedOrigins, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type, X-Property-Id, Idempotency-Key, X-Locale, X-Request-Id, Authorization, If-Match")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw, ok := w.(*statusWriter)
		if !ok {
			sw = &statusWriter{ResponseWriter: w, status: http.StatusOK}
		}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			return
		}
		m := reqctx.GetMeta(r.Context())
		lvl := slog.LevelInfo
		if sw.status >= 500 {
			lvl = slog.LevelError
		}
		slog.Log(r.Context(), lvl, "http request",
			"request_id", m.RequestID, "method", r.Method, "path", r.URL.Path, "route", m.RouteID,
			"module", m.Module, "status", sw.status, "duration_ms", time.Since(start).Milliseconds(), "ip", m.IP)
	})
}

// wrap builds the per-route pipeline.
func (s *Server) wrap(rt *route.Route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		meta := reqctx.GetMeta(ctx)
		meta.RouteID = rt.ID()
		meta.Module = rt.Module

		sw, ok := w.(*statusWriter)
		if !ok {
			sw = &statusWriter{ResponseWriter: w, status: http.StatusOK}
		}

		ctx, err := s.authorize(ctx, r, rt)
		if err != nil {
			httpx.WriteError(sw, r.WithContext(ctx), err)
			return
		}

		var tracker *reqctx.AuditTracker
		if rt.Mutating() {
			tracker = &reqctx.AuditTracker{}
			ctx = reqctx.WithAuditTracker(ctx, tracker)
		}
		r = r.WithContext(ctx)

		if rt.Idempotent && r.Header.Get("Idempotency-Key") != "" {
			s.withIdempotency(sw, r, rt.Handler)
		} else {
			rt.Handler(sw, r)
		}

		if rt.Mutating() && sw.status >= 200 && sw.status < 300 {
			s.mu.Lock()
			if s.exercised == nil {
				s.exercised = map[string]bool{}
			}
			s.exercised[rt.ID()] = true
			replayed := sw.Header().Get("Idempotent-Replayed") == "true"
			if rt.NoAudit == "" && tracker.Count == 0 && !replayed {
				s.violations = append(s.violations, rt.ID())
				slog.ErrorContext(ctx, "mutating request succeeded without audit entry", "route", rt.ID())
			}
			s.mu.Unlock()
		}
	})
}

// authorize authenticates and computes authz + RLS scope for rt.
func (s *Server) authorize(ctx context.Context, r *http.Request, rt *route.Route) (context.Context, error) {
	if rt.Auth == route.AuthSignature {
		return dbtx.WithScope(ctx, dbtx.Scope{}), nil
	}
	p, err := s.Auth.Authenticate(ctx, r)
	if err != nil {
		return ctx, err
	}
	if p != nil {
		ctx = authz.WithPrincipal(ctx, p)
		if p.Locale != "" {
			reqctx.GetMeta(ctx).Locale = p.Locale
		}
	}
	if reqctx.GetMeta(ctx).Locale == "" {
		reqctx.GetMeta(ctx).Locale = "en"
	}
	if rt.Auth == route.AuthPublic {
		scope := dbtx.Scope{}
		if p != nil {
			scope.UserID = p.UserID
		}
		return dbtx.WithScope(ctx, scope), nil
	}
	if p == nil {
		return ctx, errs.Unauthorized("authentication required")
	}
	if p.MFAPending && rt.Auth != route.AuthMFAPending {
		return ctx, errs.MFARequired("complete multi-factor authentication first")
	}
	if p.PasswordChangeRequired && rt.Auth != route.AuthMFAPending {
		e := errs.Forbidden("change your temporary password first")
		e.Code = "password_change_required"
		return ctx, e
	}

	// CSRF defence for cookie sessions: browsers always send Origin on
	// cross-site mutations; it must be one of the OneClub app origins.
	if rt.Mutating() && p.Kind != authz.ActorAPIKey {
		if o := r.Header.Get("Origin"); o != "" && !slices.Contains(s.Cfg.AllowedOrigins, o) {
			return ctx, errs.Forbidden("origin not allowed")
		}
	}

	if rt.Module != "platform" && rt.Module != "audit" {
		on, err := s.Gate.ModuleEnabled(ctx, rt.Module)
		if err != nil {
			return ctx, err
		}
		if !on {
			return ctx, errs.ModuleDisabled(rt.Module)
		}
	}
	if !p.Can(catalog.ShellPlatformAdmin, nil) && rt.Auth == route.AuthRequired {
		if susp, err := s.Gate.Suspended(ctx); err != nil {
			return ctx, err
		} else if susp {
			return ctx, errs.Unavailable("this customer instance is suspended")
		}
	}

	scope := dbtx.Scope{UserID: p.UserID}
	propHeader := r.Header.Get("X-Property-Id")
	var active uuid.UUID
	if propHeader != "" {
		u, err := uuid.Parse(propHeader)
		if err != nil {
			return ctx, errs.BadRequest("invalid_property", "X-Property-Id must be a UUID")
		}
		active = u
	}

	switch rt.Scope {
	case route.ScopeProperty:
		if active == uuid.Nil {
			return ctx, errs.BadRequest("property_required", "select a property (X-Property-Id header)")
		}
		if !p.CanAccessProperty(active) {
			return ctx, errs.Forbidden("no access to this property")
		}
		if rt.Permission != "" && !p.Can(rt.Permission, &active) {
			return ctx, errs.Forbidden("missing permission " + rt.Permission)
		}
		scope.PropertyIDs = []uuid.UUID{active}
		ctx = reqctx.WithProperty(ctx, active)
	default:
		if rt.Permission != "" && !p.Can(rt.Permission, nil) {
			return ctx, errs.Forbidden("missing permission " + rt.Permission)
		}
		scope.AllProperties, scope.PropertyIDs = p.PropertiesFor(rt.Permission)
		if active != uuid.Nil && p.CanAccessProperty(active) {
			ctx = reqctx.WithProperty(ctx, active)
		}
	}
	return dbtx.WithScope(ctx, scope), nil
}

// MarshalSpec renders the OpenAPI document as indented JSON.
func MarshalSpec(reg *route.Registry, info route.Info) ([]byte, error) {
	doc, err := reg.BuildOpenAPI(info)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "  ")
}
