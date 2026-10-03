// Package iam implements Identity & Access (EP-03): sessions, login with
// lockout, TOTP MFA, password reset, device + PIN login, users, roles,
// per-property role assignments, devices and API keys.
package iam

import (
	"context"
	"net/http"
	"sync"
	"time"

	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/notify"
)

// SessionCookie is the opaque session cookie name (FR-IAM-02).
const SessionCookie = "oneclub_session"

// Lockout policy (FR-IAM-04).
const (
	MaxFailedLogins = 5
	LockoutDuration = 15 * time.Minute
	ResetTokenTTL   = 30 * time.Minute
	InviteTokenTTL  = 72 * time.Hour
	DeviceShiftTTL  = 12 * time.Hour
)

// Service is the IAM module.
type Service struct {
	DB      *dbtx.DB
	Cfg     *config.Config
	Box     *secret.Box
	Notify  notify.Sender
	Catalog *catalog.Catalog

	limiter *limiter
}

// New builds the IAM service.
func New(db *dbtx.DB, cfg *config.Config, box *secret.Box, n notify.Sender, cat *catalog.Catalog) *Service {
	return &Service{DB: db, Cfg: cfg, Box: box, Notify: n, Catalog: cat, limiter: newLimiter()}
}

func respond(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if v == nil {
		httpx.NoContent(w)
		return
	}
	httpx.JSON(w, status, v)
}

// limiter is a small fixed-window rate limiter for authentication endpoints
// (Technical Doc §9.1: login protected by rate limit and lockout). It is
// per-process; with several API replicas the effective limit scales, which
// is acceptable because the per-account lockout is enforced in the database.
type limiter struct {
	mu   sync.Mutex
	hits map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newLimiter() *limiter { return &limiter{hits: map[string]*window{}} }

// allow reports whether key may proceed (max n per period).
func (l *limiter) allow(key string, n int, period time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) > period {
		if len(l.hits) > 50000 {
			l.hits = map[string]*window{}
		}
		l.hits[key] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= n
}

func (s *Service) rateLimit(key string, n int, period time.Duration) error {
	if s.Cfg.Env == "test" {
		return nil
	}
	if !s.limiter.allow(key, n, period) {
		return errs.RateLimited()
	}
	return nil
}

type ctxKey int

const tokenKey ctxKey = 0

func withToken(ctx context.Context, tokenHash string) context.Context {
	return context.WithValue(ctx, tokenKey, tokenHash)
}
