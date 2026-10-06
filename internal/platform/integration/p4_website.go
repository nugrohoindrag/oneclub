package integration

// Public website cache invalidation (PRD P4 FR-CMS-10, EP-25): the website
// (Next.js, web/apps/web) serves CMS content from its data cache; when
// content is published or website master data changes, OneClub asks the
// website to revalidate the affected paths and the "cms" cache tag at once
// (on-demand revalidation). The request is signed with HMAC-SHA256 over
// "<unix seconds>.<body>" in X-OneClub-Signature (t=…,v1=…), the same
// scheme as the sandbox webhooks; the website verifies it with the shared
// secret ONECLUB_REVALIDATE_SECRET and rejects stale (> 5 min) requests.
// Without a website integration the website still refreshes by time.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CapWebsite is the public website capability.
const CapWebsite = "website"

// RevalidateRequest asks the website to refresh cached pages.
type RevalidateRequest struct {
	PropertyID string   `json:"propertyId"`
	Revision   int64    `json:"revision"`
	Paths      []string `json:"paths"`
	Tags       []string `json:"tags"`
	Event      string   `json:"event"`
}

// WebsiteAdapter is the website capability.
type WebsiteAdapter interface {
	Revalidate(ctx context.Context, r RevalidateRequest) error
}

func init() {
	RegisterAdapter(AdapterInfo{
		Key: "nextjs-website", Capability: CapWebsite, Name: "Website (Next.js on-demand revalidation)",
		Description: "Refreshes the public website cache when CMS content is published or website data changes: a signed POST to " +
			"<website>/api/revalidate (HMAC-SHA256, header X-OneClub-Signature). Set the same secret as ONECLUB_REVALIDATE_SECRET on the website.",
		Credentials: []Field{{Key: "revalidateSecret", Label: "Revalidation secret", Type: "secret", Required: true,
			Help: "Shared with the website (ONECLUB_REVALIDATE_SECRET), at least 32 characters"}},
		Settings: []Field{
			{Key: "baseUrl", Label: "Website base URL", Type: "url", Required: true, Help: "e.g. https://www.moderngolf.co.id"},
			{Key: "path", Label: "Revalidation path", Type: "string", Help: "Default /api/revalidate"},
		},
		New: func(env Env) (any, error) { return newNextWebsite(env) },
	})
	RegisterAdapter(AdapterInfo{
		Key: "mock-website", Capability: CapWebsite, Name: "Mock Website", Sandbox: true,
		Description: "Records revalidation requests in the integration log (development without a website).",
		Settings:    []Field{{Key: "alwaysFail", Label: "Always fail (testing retries)", Type: "boolean"}},
		New:         func(env Env) (any, error) { return &mockWebsite{env: env}, nil },
	})
}

// Website resolves the website capability; ErrNotConfigured when none.
func (s *Service) Website(ctx context.Context) (WebsiteAdapter, string, error) {
	a, code, err := s.Resolve(ctx, CapWebsite)
	if err != nil {
		return nil, "", err
	}
	w, ok := a.(WebsiteAdapter)
	if !ok {
		return nil, "", fmt.Errorf("integration %s does not implement website", code)
	}
	return w, code, nil
}

type nextWebsite struct {
	env    Env
	target string
	secret string
}

func newNextWebsite(env Env) (*nextWebsite, error) {
	base := strings.TrimRight(setting(env, "baseUrl", ""), "/")
	u, err := url.Parse(base)
	if base == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("website: base URL must be an http(s) address")
	}
	sec := env.Credentials["revalidateSecret"]
	if len(sec) < 16 {
		return nil, errors.New("website: the revalidation secret must have at least 16 characters")
	}
	p := setting(env, "path", "/api/revalidate")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return &nextWebsite{env: env, target: base + p, secret: sec}, nil
}

// revalidateResponse is the answer of the website route handler.
type revalidateResponse struct {
	Revalidated bool     `json:"revalidated"`
	Paths       []string `json:"paths"`
	Tags        []string `json:"tags"`
}

func (w *nextWebsite) send(ctx context.Context, op string, r RevalidateRequest) error {
	if r.Paths == nil {
		r.Paths = []string{}
	}
	if len(r.Tags) == 0 {
		r.Tags = []string{"cms"}
	}
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	hdr := http.Header{SignatureHeader: {Sign(w.secret, body, time.Now())}}
	var out revalidateResponse
	if _, err := p4Call(ctx, w.env, op, http.MethodPost, w.target, hdr, body, nil, nil, &out); err != nil {
		return err
	}
	if !out.Revalidated {
		return fmt.Errorf("%s: the website did not confirm the revalidation", op)
	}
	return nil
}

// Revalidate asks the website to refresh the paths (and the "cms" tag).
func (w *nextWebsite) Revalidate(ctx context.Context, r RevalidateRequest) error {
	return w.send(ctx, "revalidate", r)
}

// TestConnection sends a signed, empty revalidation (tags only).
func (w *nextWebsite) TestConnection(ctx context.Context) (string, error) {
	if err := w.send(ctx, "test_connection", RevalidateRequest{Event: "test_connection", Tags: []string{"cms"}}); err != nil {
		return "", err
	}
	return "Website reachable and the signature accepted (" + w.target + ")", nil
}

// ── sandbox ──────────────────────────────────────────────────────────────

var (
	mockWebsiteMu    sync.Mutex
	mockWebsiteCalls []RevalidateRequest
)

type mockWebsite struct{ env Env }

func (m *mockWebsite) Revalidate(ctx context.Context, r RevalidateRequest) error {
	return timed(m.env, ctx, "revalidate", r, func() (any, int, error) {
		if v, _ := m.env.Settings["alwaysFail"].(bool); v {
			return nil, 503, errors.New("simulated website outage")
		}
		mockWebsiteMu.Lock()
		mockWebsiteCalls = append(mockWebsiteCalls, r)
		mockWebsiteMu.Unlock()
		return map[string]any{"revalidated": true, "paths": len(r.Paths)}, 200, nil
	})
}

func (m *mockWebsite) TestConnection(ctx context.Context) (string, error) {
	return "Mock website ready", timed(m.env, ctx, "test_connection", nil, func() (any, int, error) { return map[string]any{"ok": true}, 200, nil })
}

// MockWebsiteCalls returns the revalidations received by the sandbox
// website (tests).
func MockWebsiteCalls() []RevalidateRequest {
	mockWebsiteMu.Lock()
	defer mockWebsiteMu.Unlock()
	return append([]RevalidateRequest(nil), mockWebsiteCalls...)
}
