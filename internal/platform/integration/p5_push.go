package integration

// Push notifications of the PWAs (PRD P5 FR-INT-P5-05, FR-ESS-07): the
// Staff App (Employee Self Service) and the Member App subscribe with the
// browser Push API; the notification service delivers the "push" channel
// through this capability. Adapters: "webpush" sends standard Web Push
// (RFC 8030) with message encryption (RFC 8291, aes128gcm) and VAPID
// authentication (RFC 8292) — no third-party library; "mock-push" records
// the pushes in the integration log (trial / development). Without a push
// integration a log pusher is used outside production, with a VAPID key
// derived from the instance secret so browsers can still subscribe.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CapPush is the push notification capability.
const CapPush = "push"

// PushTarget is one browser subscription (PushSubscription.toJSON()).
type PushTarget struct {
	Endpoint string
	P256DH   string // base64url, uncompressed P-256 point of the browser
	Auth     string // base64url, 16-byte authentication secret
}

// PushMessage is the notification shown by the service worker.
type PushMessage struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Link  string `json:"link,omitempty"`
	Tag   string `json:"tag,omitempty"`
	TTL   int    `json:"-"` // seconds the push service keeps it (default 24 h)
}

// ErrPushGone means the subscription expired or was revoked (HTTP 404 /
// 410): the caller deletes it.
var ErrPushGone = errors.New("push subscription is gone")

// PushAdapter is the push capability.
type PushAdapter interface {
	// SendPush delivers one message to one subscription.
	SendPush(ctx context.Context, t PushTarget, m PushMessage) error
	// PublicKey is the VAPID application server key (base64url,
	// uncompressed point) the browsers subscribe with.
	PublicKey() string
}

func init() {
	RegisterAdapter(AdapterInfo{
		Key: "webpush", Capability: CapPush, Name: "Web Push (VAPID)",
		Description: "Standard Web Push to the installed Staff App (Employee Self Service) and Member App: messages are encrypted for each " +
			"browser (RFC 8291) and signed with the club's VAPID key (RFC 8292). Works with Chrome / Android, Safari / iOS 16.4+ (installed PWA) and Firefox.",
		Credentials: []Field{{Key: "vapidPrivateKey", Label: "VAPID private key", Type: "secret", Required: true,
			Help: "base64url of the 32-byte P-256 private key (e.g. `npx web-push generate-vapid-keys`, private key)"}},
		Settings: []Field{{Key: "subject", Label: "Contact (VAPID subject)", Type: "string", Required: true, Help: "mailto:it@club.example or https URL"}},
		New: func(env Env) (any, error) {
			key, err := ParseVAPIDKey(env.Credentials["vapidPrivateKey"])
			if err != nil {
				return nil, err
			}
			sub, _ := env.Settings["subject"].(string)
			return &webPush{env: env, key: key, subject: sub, client: &http.Client{Timeout: 15 * time.Second}}, nil
		},
	})
	RegisterAdapter(AdapterInfo{
		Key: "mock-push", Capability: CapPush, Name: "Mock Push", Sandbox: true,
		Description: "Records push notifications in the integration log instead of sending them (trial and development).",
		Settings:    []Field{{Key: "alwaysFail", Label: "Always fail (testing retries)", Type: "boolean"}},
		New: func(env Env) (any, error) {
			return &logPusher{env: env, key: devVAPIDKey("mock-push")}, nil
		},
	})
}

// Pusher resolves the push capability: the enabled push integration, else
// (outside production) a log pusher.
func (s *Service) Pusher(ctx context.Context) (PushAdapter, error) {
	a, code, err := s.Resolve(ctx, CapPush)
	if err == nil {
		p, ok := a.(PushAdapter)
		if !ok {
			return nil, fmt.Errorf("integration %s does not implement push", code)
		}
		return p, nil
	}
	if !errors.Is(err, ErrNotConfigured) {
		return nil, err
	}
	if s.Cfg != nil && s.Cfg.Env == "production" {
		return nil, ErrNotConfigured
	}
	seed := "oneclub-dev-push"
	if s.Cfg != nil {
		seed = s.Cfg.AppSecret + ":push"
	}
	return &logPusher{env: Env{Code: "log-push", Mode: "sandbox", Log: func(ctx context.Context, c Call) { s.LogCall(ctx, nil, "log-push", c) }},
		key: devVAPIDKey(seed)}, nil
}

// ParseVAPIDKey reads a base64url P-256 private key (32 bytes).
func ParseVAPIDKey(s string) (*ecdsa.PrivateKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
	if err != nil {
		return nil, errors.New("the VAPID private key is not base64url")
	}
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, errors.New("the VAPID private key is not a P-256 key")
	}
	return k, nil
}

// devVAPIDKey derives a stable P-256 key from a seed (sandbox only).
func devVAPIDKey(seed string) *ecdsa.PrivateKey {
	for i := 0; ; i++ {
		raw, _ := hkdf.Key(sha256.New, []byte(seed), nil, fmt.Sprintf("oneclub vapid %d", i), 32)
		if k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw); err == nil {
			return k
		}
	}
}

func publicKeyB64(k *ecdsa.PrivateKey) string {
	b, _ := k.PublicKey.Bytes()
	return base64.RawURLEncoding.EncodeToString(b)
}

// logPusher writes pushes to the integration log (no network).
type logPusher struct {
	env Env
	key *ecdsa.PrivateKey
}

func (p *logPusher) PublicKey() string { return publicKeyB64(p.key) }

func (p *logPusher) SendPush(ctx context.Context, t PushTarget, m PushMessage) error {
	if fail, _ := p.env.Settings["alwaysFail"].(bool); fail {
		return errors.New("mock push configured to fail")
	}
	host := t.Endpoint
	if u, err := url.Parse(t.Endpoint); err == nil {
		host = u.Host
	}
	slog.InfoContext(ctx, "push (log pusher)", "service", host, "tag", m.Tag)
	if p.env.Log != nil {
		p.env.Log(ctx, Call{Operation: "send_push", Request: map[string]any{"service": host, "title": m.Title, "tag": m.Tag}})
	}
	return nil
}

// webPush sends standard Web Push.
type webPush struct {
	env     Env
	key     *ecdsa.PrivateKey
	subject string
	client  *http.Client
}

func (w *webPush) PublicKey() string { return publicKeyB64(w.key) }

func (w *webPush) SendPush(ctx context.Context, t PushTarget, m PushMessage) error {
	u, err := url.Parse(t.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ErrPushGone
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	body, err := EncryptPush(t, payload, rand.Reader)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPushGone, err)
	}
	jwt, err := VAPIDToken(w.key, u.Scheme+"://"+u.Host, w.subject, time.Now())
	if err != nil {
		return err
	}
	ttl := m.TTL
	if ttl <= 0 {
		ttl = 86400
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", fmt.Sprint(ttl))
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+w.PublicKey())
	start := time.Now()
	resp, err := w.client.Do(req) //nolint:gosec // G107/G704: endpoint is the browser's push service URL (https only, checked above)
	call := Call{Operation: "send_push", Direction: "outbound", Method: http.MethodPost, URL: u.Scheme + "://" + u.Host,
		Request: map[string]any{"service": u.Host, "tag": m.Tag}, Duration: time.Since(start)}
	if err != nil {
		call.Err = err
		w.log(ctx, call)
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	call.StatusCode = resp.StatusCode
	w.log(ctx, call)
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrPushGone
	case resp.StatusCode >= 300:
		return fmt.Errorf("push service answered %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (w *webPush) log(ctx context.Context, c Call) {
	if w.env.Log != nil {
		w.env.Log(ctx, c)
	}
}

// VAPIDToken is the ES256 JWT of RFC 8292 for a push service origin.
func VAPIDToken(k *ecdsa.PrivateKey, audience, subject string, now time.Time) (string, error) {
	if subject == "" {
		subject = "mailto:admin@oneclub.local"
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{"aud": audience, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// EncryptPush encrypts a payload for one subscription (RFC 8291, one
// aes128gcm record with a 4096-byte record size).
func EncryptPush(t PushTarget, payload []byte, random io.Reader) ([]byte, error) {
	dec := func(s string) ([]byte, error) {
		return base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
	}
	uaRaw, err := dec(t.P256DH)
	if err != nil {
		return nil, errors.New("invalid p256dh key")
	}
	authSecret, err := dec(t.Auth)
	if err != nil || len(authSecret) < 16 {
		return nil, errors.New("invalid auth secret")
	}
	if len(payload) > 3993 {
		return nil, errors.New("push payload too large")
	}
	curve := ecdh.P256()
	ua, err := curve.NewPublicKey(uaRaw)
	if err != nil {
		return nil, errors.New("invalid p256dh key")
	}
	as, err := curve.GenerateKey(random)
	if err != nil {
		return nil, err
	}
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, shared, authSecret)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaRaw)+string(asPub), 32)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain := append(append([]byte{}, payload...), 0x02) // last record, no padding
	out := make([]byte, 0, 16+4+1+len(asPub)+len(plain)+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, 4096)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	return gcm.Seal(out, nonce, plain, nil), nil
}
