// Package reqctx carries per-request metadata through context.Context:
// request id, client IP/user agent, active property and locale.
package reqctx

import (
	"context"

	"github.com/google/uuid"
)

type ctxKey int

const (
	metaKey ctxKey = iota
	propertyKey
	auditTrackerKey
)

// Meta is request metadata recorded in logs and audit entries.
type Meta struct {
	RequestID string
	IP        string
	UserAgent string
	Locale    string // "en" | "id"
	RouteID   string
	Module    string
}

func WithMeta(ctx context.Context, m *Meta) context.Context {
	return context.WithValue(ctx, metaKey, m)
}

// GetMeta never returns nil.
func GetMeta(ctx context.Context) *Meta {
	if m, ok := ctx.Value(metaKey).(*Meta); ok && m != nil {
		return m
	}
	return &Meta{Locale: "en"}
}

// WithProperty sets the active property (property switcher, X-Property-Id).
func WithProperty(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, propertyKey, id)
}

// Property returns the active property, if any.
func Property(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(propertyKey).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// AuditTracker counts audit entries written during one request so the route
// middleware can verify every mutation was audited (FR-AUD-01, EP-07 AC).
type AuditTracker struct{ Count int }

func WithAuditTracker(ctx context.Context, t *AuditTracker) context.Context {
	return context.WithValue(ctx, auditTrackerKey, t)
}

func GetAuditTracker(ctx context.Context) *AuditTracker {
	t, _ := ctx.Value(auditTrackerKey).(*AuditTracker)
	return t
}
