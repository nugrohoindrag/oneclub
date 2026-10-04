package crm

// Website & Non-Member Booking (PRD P2 EP-26): shared plumbing of public
// write endpoints — property scope, rate limit and honeypot bot protection,
// customer dedup (FR-WEB-P2-08) — and the general contact form.

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// PublicGuest identifies a website visitor.
type PublicGuest struct {
	Name    string `json:"name"`
	Phone   string `json:"phone"`
	Email   string `json:"email"`
	Website string `json:"website,omitempty" doc:"Honeypot — must stay empty (bot protection)"`
}

// PublicRequest is implemented by public request bodies.
type PublicRequest interface {
	Property() uuid.UUID
	Visitor() PublicGuest
}

// PublicLimiter limits public writes per client IP (FR-WEB-P2-08).
var PublicLimiter = &handle.Limiter{N: 20, Period: time.Minute}

func clientIP(r *http.Request) string {
	if m := reqctx.GetMeta(r.Context()); m != nil && m.IP != "" {
		return m.IP
	}
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	return h
}

// PublicCtx scopes a context to a property for anonymous requests.
func PublicCtx(ctx context.Context, pid uuid.UUID) context.Context {
	return reqctx.WithProperty(dbtx.WithScope(ctx, dbtx.Scope{PropertyIDs: []uuid.UUID{pid}}), pid)
}

// PublicWrite runs a public write: validates the visitor (honeypot, name,
// phone or e-mail), dedups the customer and calls fn in one transaction.
func PublicWrite[Req PublicRequest, Res any](db *dbtx.DB, status int, fn func(ctx context.Context, tx pgx.Tx, r *http.Request, property uuid.UUID, customer Customer, in Req) (Res, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !PublicLimiter.Allow(clientIP(r) + r.URL.Path) {
			httpx.WriteError(w, r, errs.RateLimited())
			return
		}
		var in Req
		if err := httpx.Decode(r, &in); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		g := in.Visitor()
		if g.Website != "" {
			// Bots fill every field; accept silently, keep only an audit trace.
			if pid := in.Property(); pid != uuid.Nil {
				ctx := PublicCtx(r.Context(), pid)
				_ = db.WithTx(ctx, func(tx pgx.Tx) error {
					return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "bot_dropped", EntityType: "public.request", EntityID: r.URL.Path,
						EntityLabel: clientIP(r), PropertyID: &pid})
				})
			}
			httpx.JSON(w, http.StatusAccepted, map[string]any{"status": "received"})
			return
		}
		g.Name = strings.TrimSpace(g.Name)
		if g.Name == "" || (strings.TrimSpace(g.Phone) == "" && strings.TrimSpace(g.Email) == "") {
			httpx.WriteError(w, r, handle.Invalid("guest", "required", "name and phone or e-mail are required"))
			return
		}
		pid := in.Property()
		if pid == uuid.Nil {
			httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
			return
		}
		ctx := PublicCtx(r.Context(), pid)
		var out Res
		err := db.WithTx(ctx, func(tx pgx.Tx) error {
			c, _, err := FindOrCreate(ctx, tx, pid, Identity{Name: g.Name, Phone: g.Phone, Email: g.Email})
			if err != nil {
				return err
			}
			out, err = fn(ctx, tx, r, pid, c, in)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, status, out)
	}
}

// PublicRoute registers an anonymous website route.
func PublicRoute(reg *route.Registry, module string, rt route.Route) {
	rt.Module, rt.Tag, rt.Auth = module, "Public", route.AuthPublic
	reg.Add(rt)
}

// ContactInput is the general contact form (PRD P2 §6 #14).
type ContactInput struct {
	PropertyID uuid.UUID   `json:"propertyId"`
	Guest      PublicGuest `json:"guest"`
	Topic      string      `json:"topic" doc:"e.g. membership, bungalow, meeting, other"`
	Message    string      `json:"message"`
}

func (c ContactInput) Property() uuid.UUID  { return c.PropertyID }
func (c ContactInput) Visitor() PublicGuest { return c.Guest }

type ContactResult struct {
	Status string `json:"status"`
}

func (m *Module) registerPublic(reg *route.Registry) {
	PublicRoute(reg, "crm", route.Route{Method: http.MethodPost, Path: "/api/v1/public/contact", Summary: "Contact form (logged as a customer interaction)",
		Request: ContactInput{}, Response: ContactResult{}, Status: http.StatusAccepted,
		Handler: PublicWrite(m.DB, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c Customer, in ContactInput) (ContactResult, error) {
			if err := handle.Required("message", in.Message); err != nil {
				return ContactResult{}, err
			}
			topic := in.Topic
			if topic == "" {
				topic = "general"
			}
			_, err := LogInteraction(ctx, tx, pid, c.ID, InteractionInput{Channel: "other", Direction: "inbound", Subject: "Website contact · " + topic,
				Body: in.Message}, "manual", "website.contact", nil)
			return ContactResult{Status: "received"}, err
		})})
}
