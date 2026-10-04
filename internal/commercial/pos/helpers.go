package pos

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/realtime"
)

func actorID(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

// publishRT notifies SSE subscribers of commercial.<topic> (KDS, order
// status) when tx commits; payloads carry ids and status only.
func publishRT(ctx context.Context, tx pgx.Tx, topic string, property uuid.UUID, kind, ref string, data map[string]any) error {
	payload := map[string]any{"id": ref}
	for k, v := range data {
		payload[k] = v
	}
	return realtime.Publish(ctx, tx, "commercial."+topic, kind, &property, payload)
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
