package voucher

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/realtime"
	"oneclub/internal/platform/resource"
)

func actorID(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func chiParam(r *http.Request, name string) string { return chi.URLParam(r, name) }

// codeField is the code of P2 commercial masters (voucher types …): up to
// 40 foundation-code characters.
func codeField(label string) resource.Field {
	return resource.Field{Name: "code", Column: "code", Label: label, Kind: resource.String, Required: true, Max: 40, Upper: true,
		Pattern: resource.FoundationCode, PatternMsg: "1–40 characters: letters, digits, . _ / -", Search: true, Filter: true}
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
