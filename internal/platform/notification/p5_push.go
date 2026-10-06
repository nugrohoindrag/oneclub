package notification

// Push notifications (PRD P5 FR-INT-P5-05, FR-ESS-07): the installed Staff
// App (Employee Self Service) and Member App register their browser push
// subscription; the "push" channel delivers through the integration
// capability "push" (Web Push with VAPID, or the mock / log pusher).
// Expired subscriptions (404 / 410 from the push service) are revoked.

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/integration"
)

// PushConfig is what a PWA needs to subscribe.
type PushConfig struct {
	Enabled   bool   `json:"enabled" doc:"false when no push integration is configured (production)"`
	PublicKey string `json:"publicKey" doc:"VAPID application server key (base64url) for PushManager.subscribe"`
}

// PushSubscriptionKeys are the browser keys of a subscription.
type PushSubscriptionKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// PushSubscriptionRequest registers this device (PushSubscription.toJSON()
// without expirationTime).
type PushSubscriptionRequest struct {
	Endpoint  string               `json:"endpoint"`
	Keys      PushSubscriptionKeys `json:"keys"`
	Surface   string               `json:"surface" enum:"staff,member"`
	UserAgent string               `json:"userAgent,omitempty"`
}

// PushSubscriptionView is a subscribed device of the user.
type PushSubscriptionView struct {
	ID            uuid.UUID  `json:"id"`
	Surface       string     `json:"surface" enum:"staff,member"`
	Service       string     `json:"service" doc:"Host of the browser push service"`
	UserAgent     *string    `json:"userAgent"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastSuccessAt *time.Time `json:"lastSuccessAt"`
}

func (h *HTTP) registerPush(add func(route.Route)) {
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/push-config", Summary: "Push notification key of this instance (VAPID)",
		Response: PushConfig{}, Handler: h.pushConfig})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/push-subscriptions", Summary: "My devices subscribed to push notifications",
		Response: PushSubscriptionView{}, List: true, Handler: h.pushSubscriptions})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/push-subscriptions", Summary: "Subscribe this device to push notifications",
		Request: PushSubscriptionRequest{}, Response: PushSubscriptionView{}, Status: http.StatusCreated, Handler: h.subscribePush})
	add(route.Route{Method: http.MethodDelete, Path: "/api/v1/platform/push-subscriptions/{id}", Summary: "Unsubscribe a device from push notifications",
		Handler: h.unsubscribePush})
}

func (h *HTTP) pushConfig(w http.ResponseWriter, r *http.Request) {
	p, err := h.Svc.Integrations.Pusher(r.Context())
	if errors.Is(err, integration.ErrNotConfigured) {
		httpx.JSON(w, http.StatusOK, PushConfig{})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, PushConfig{Enabled: true, PublicKey: p.PublicKey()})
}

const pushSelect = `SELECT id, surface, substring(endpoint FROM '^https://([^/]+)'), user_agent, created_at, last_success_at
	FROM platform.push_subscriptions`

func scanPush(row pgx.Row) (PushSubscriptionView, error) {
	var v PushSubscriptionView
	err := row.Scan(&v.ID, &v.Surface, &v.Service, &v.UserAgent, &v.CreatedAt, &v.LastSuccessAt)
	return v, err
}

func (h *HTTP) pushSubscriptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []PushSubscriptionView{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, pushSelect+` WHERE user_id = $1 AND revoked_at IS NULL ORDER BY created_at DESC`, authz.From(ctx).UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanPush(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[PushSubscriptionView]{Items: out})
}

func (h *HTTP) subscribePush(w http.ResponseWriter, r *http.Request) {
	var req PushSubscriptionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if !strings.HasPrefix(req.Endpoint, "https://") || len(req.Endpoint) > 2048 {
		httpx.WriteError(w, r, errs.Validation("invalid_endpoint", "invalid push endpoint", errs.Field("endpoint", "invalid", "an https push service URL")))
		return
	}
	if req.Surface != "staff" && req.Surface != "member" {
		httpx.WriteError(w, r, errs.Validation("invalid_surface", "invalid surface", errs.Field("surface", "invalid", "staff or member")))
		return
	}
	// the keys must form a valid encryption target
	if _, err := integration.EncryptPush(integration.PushTarget{Endpoint: req.Endpoint, P256DH: req.Keys.P256DH, Auth: req.Keys.Auth}, []byte("{}"),
		rand.Reader); err != nil {
		httpx.WriteError(w, r, errs.Validation("invalid_keys", "invalid subscription keys", errs.Field("keys", "invalid", err.Error())))
		return
	}
	ua := strings.TrimSpace(req.UserAgent)
	if len(ua) > 300 {
		ua = ua[:300]
	}
	var out PushSubscriptionView
	err := h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var sid uuid.UUID
		// one row per endpoint: a device signed in by another user moves to this user
		if err := tx.QueryRow(ctx, `INSERT INTO platform.push_subscriptions (id, user_id, endpoint, p256dh, auth, surface, user_agent)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (endpoint) DO UPDATE SET user_id = EXCLUDED.user_id, p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth,
			surface = EXCLUDED.surface, user_agent = EXCLUDED.user_agent, revoked_at = NULL, failure_count = 0 RETURNING id`,
			id.New(), p.UserID, req.Endpoint, req.Keys.P256DH, req.Keys.Auth, req.Surface, nullable(ua)).Scan(&sid); err != nil {
			return err
		}
		v, err := scanPush(tx.QueryRow(ctx, pushSelect+` WHERE id = $1`, sid))
		if err != nil {
			return err
		}
		out = v
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "push_subscribe", EntityType: "platform.push_subscription",
			EntityID: sid.String(), EntityLabel: p.Name + " · " + v.Service, After: map[string]any{"surface": req.Surface, "service": v.Service}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *HTTP) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authz.From(ctx)
	sid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.push_subscriptions SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
			sid, p.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errs.NotFound("push subscription")
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "push_unsubscribe", EntityType: "platform.push_subscription",
			EntityID: sid.String(), EntityLabel: p.Name})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// deliverPush sends a push delivery to every subscribed device of its
// user; it returns the devices reached, a skip reason (no device, no push
// integration) or the error to retry.
func (s *Service) deliverPush(ctx context.Context, deliveryID uuid.UUID, event, subject, body, link string) (int, string, error) {
	var user *uuid.UUID
	if err := s.DB.Primary.QueryRow(ctx, `SELECT user_id FROM platform.notification_deliveries WHERE id = $1`, deliveryID).Scan(&user); err != nil {
		return 0, "", err
	}
	if user == nil {
		return 0, "recipient has no account", nil
	}
	pusher, err := s.Integrations.Pusher(ctx)
	if errors.Is(err, integration.ErrNotConfigured) {
		return 0, "push is not configured", nil
	}
	if err != nil {
		return 0, "", err
	}
	rows, err := s.DB.Primary.Query(ctx, `SELECT id, endpoint, p256dh, auth FROM platform.push_subscriptions WHERE user_id = $1 AND revoked_at IS NULL`, *user)
	if err != nil {
		return 0, "", err
	}
	type sub struct {
		id uuid.UUID
		t  integration.PushTarget
	}
	var subs []sub
	for rows.Next() {
		var x sub
		if err := rows.Scan(&x.id, &x.t.Endpoint, &x.t.P256DH, &x.t.Auth); err != nil {
			rows.Close()
			return 0, "", err
		}
		subs = append(subs, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	sent := 0
	var lastErr error
	for _, x := range subs {
		err := pusher.SendPush(ctx, x.t, integration.PushMessage{Title: subject, Body: body, Link: link, Tag: event})
		switch {
		case errors.Is(err, integration.ErrPushGone):
			_, _ = s.DB.Primary.Exec(ctx, `UPDATE platform.push_subscriptions SET revoked_at = now() WHERE id = $1`, x.id)
		case err != nil:
			lastErr = err
			_, _ = s.DB.Primary.Exec(ctx, `UPDATE platform.push_subscriptions SET failure_count = failure_count + 1 WHERE id = $1`, x.id)
		default:
			sent++
			_, _ = s.DB.Primary.Exec(ctx, `UPDATE platform.push_subscriptions SET last_success_at = now(), failure_count = 0 WHERE id = $1`, x.id)
		}
	}
	if sent == 0 && lastErr != nil {
		return 0, "", lastErr
	}
	if sent == 0 {
		return 0, "no subscribed device", nil
	}
	return sent, "", nil
}
