// Package integration is the Integration Layer (EP-08): one interface per
// capability, one implementation (adapter) per vendor, configured per
// customer instance with encrypted credentials, sandbox/production mode,
// masked call logs, signed idempotent webhooks and local bridge agents.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/secret"
)

// Capabilities (FR-INT-01).
const (
	CapPayment      = "payment"
	CapMessaging    = "messaging"
	CapEmail        = "email"
	CapTaxInvoice   = "tax_invoice"
	CapResidentData = "resident_data"
	CapHardware     = "hardware"
)

// ── Capability interfaces ─────────────────────────────────────────────────

type PaymentRequest struct {
	Reference   string
	Amount      string // decimal string
	Currency    string
	Method      string // qris | virtual_account | card
	Description string
	CustomerRef string
}

type PaymentResult struct {
	ExternalID  string
	Status      string // pending | paid | failed
	CheckoutURL string
	QRString    string
	VANumber    string
}

// PaymentAdapter is the payment gateway capability (QRIS, VA, card).
type PaymentAdapter interface {
	CreatePayment(ctx context.Context, r PaymentRequest) (PaymentResult, error)
	GetPayment(ctx context.Context, externalID string) (PaymentResult, error)
}

type OutboundMessage struct {
	To         string // E.164 phone
	Template   string // OneClub event code; adapters map it to the approved BSP template
	Language   string
	Parameters []string
	Named      map[string]string // template variables by name (notification data)
	Text       string            // rendered text (session message / sandbox)
}

type MessageResult struct {
	ExternalID string
	Status     string
}

// MessagingAdapter is the WhatsApp Business capability.
type MessagingAdapter interface {
	SendMessage(ctx context.Context, m OutboundMessage) (MessageResult, error)
}

type Email struct {
	To       string
	ToName   string
	FromName string
	Subject  string
	Text     string
	HTML     string
}

// EmailAdapter sends transactional e-mail.
type EmailAdapter interface {
	SendEmail(ctx context.Context, e Email) error
}

type TaxInvoice struct {
	Reference   string
	BuyerNPWP   string
	BuyerName   string
	TotalAmount string
	TaxAmount   string
	IssuedAt    time.Time
}

type TaxInvoiceResult struct {
	ExternalID string
	Number     string
	Status     string
}

// TaxInvoiceAdapter is the e-Faktur (Coretax) capability.
type TaxInvoiceAdapter interface {
	SubmitInvoice(ctx context.Context, inv TaxInvoice) (TaxInvoiceResult, error)
}

type Resident struct {
	ResidentID string
	Name       string
	Address    string
	Status     string
}

// ResidentDataAdapter looks up Modernland resident data.
type ResidentDataAdapter interface {
	LookupResident(ctx context.Context, query string) ([]Resident, error)
}

type HardwareCommand struct {
	AgentID  uuid.UUID
	Device   string
	Command  string
	Payload  map[string]any
	Deadline time.Time
}

// HardwareAdapter sends commands to hardware through a bridge agent.
type HardwareAdapter interface {
	SendCommand(ctx context.Context, c HardwareCommand) error
}

// WebhookEvent is a verified inbound webhook.
type WebhookEvent struct {
	ExternalID string
	Type       string
	Data       map[string]any
}

// WebhookVerifier is implemented by adapters that receive webhooks
// (FR-INT-03). Verify must reject unsigned or tampered requests.
type WebhookVerifier interface {
	VerifyWebhook(h http.Header, body []byte, secret string, now time.Time) (WebhookEvent, error)
}

// Tester is implemented by adapters that can check connectivity.
type Tester interface {
	TestConnection(ctx context.Context) (string, error)
}

// ErrInvalidSignature is returned by VerifyWebhook.
var ErrInvalidSignature = errors.New("invalid webhook signature")

// ── Adapter registry ──────────────────────────────────────────────────────

// Field describes a credential or setting input in Settings → Integrations.
type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type" enum:"string,secret,number,boolean,url"`
	Required bool   `json:"required"`
	Help     string `json:"help,omitempty"`
}

// Env is passed to adapter constructors.
type Env struct {
	Code        string
	Mode        string // sandbox | production
	Credentials map[string]string
	Settings    map[string]any
	Log         CallLogger
}

// AdapterInfo registers a vendor implementation.
type AdapterInfo struct {
	Key         string                     `json:"key"`
	Capability  string                     `json:"capability"`
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Sandbox     bool                       `json:"sandbox" doc:"Mock / sandbox adapter for development (FR-INT-05)"`
	Credentials []Field                    `json:"credentials"`
	Settings    []Field                    `json:"settings"`
	Webhooks    bool                       `json:"webhooks"`
	New         func(env Env) (any, error) `json:"-"`
}

var (
	regMu    sync.RWMutex
	adapters = map[string]AdapterInfo{}
)

// RegisterAdapter adds a vendor adapter (called from init or composition).
func RegisterAdapter(a AdapterInfo) {
	regMu.Lock()
	defer regMu.Unlock()
	if a.Credentials == nil {
		a.Credentials = []Field{}
	}
	if a.Settings == nil {
		a.Settings = []Field{}
	}
	adapters[a.Key] = a
}

// Adapters lists registered adapters.
func Adapters() []AdapterInfo {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]AdapterInfo, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Capability == out[j].Capability {
			return out[i].Key < out[j].Key
		}
		return out[i].Capability < out[j].Capability
	})
	return out
}

func adapterInfo(key string) (AdapterInfo, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	a, ok := adapters[key]
	return a, ok
}

// ── Call logging (FR-INT-04) ──────────────────────────────────────────────

// Call is one outbound or inbound exchange.
type Call struct {
	Operation     string
	Direction     string // outbound | inbound
	Method        string
	URL           string
	Request       any
	Response      any
	StatusCode    int
	Err           error
	Duration      time.Duration
	CorrelationID string
}

// CallLogger records calls for one integration.
type CallLogger func(ctx context.Context, c Call)

// Service manages integrations.
type Service struct {
	DB  *dbtx.DB
	Box *secret.Box
	Cfg *config.Config
}

func toMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]any{"value": fmt.Sprint(v)}
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return map[string]any{"value": string(raw)}
	}
	return m
}

// LogCall persists a masked call record. It uses its own connection so the
// record survives even if the caller's transaction rolls back.
func (s *Service) LogCall(ctx context.Context, integrationID *uuid.UUID, code string, c Call) {
	if c.Direction == "" {
		c.Direction = "outbound"
	}
	req, _ := json.Marshal(mask.Map(toMap(c.Request), true))
	resp, _ := json.Marshal(mask.Map(toMap(c.Response), true))
	var errStr *string
	if c.Err != nil {
		e := c.Err.Error()
		errStr = &e
	}
	var status *int
	if c.StatusCode != 0 {
		status = &c.StatusCode
	}
	_, err := s.DB.Primary.Exec(context.WithoutCancel(ctx), `
		INSERT INTO platform.integration_logs (id, integration_id, integration_code, direction, operation, method, url,
		  request, response, status_code, success, duration_ms, error, correlation_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		id.New(), integrationID, code, c.Direction, c.Operation, nullable(c.Method), nullable(c.URL), req, resp, status,
		c.Err == nil, c.Duration.Milliseconds(), errStr, nullable(c.CorrelationID))
	if err != nil {
		slog.WarnContext(ctx, "integration log write failed", "err", err, "integration", code)
	}
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// record is the persisted integration configuration.
type record struct {
	ID          uuid.UUID
	Code        string
	Adapter     string
	Capability  string
	Name        string
	Enabled     bool
	Mode        string
	Credentials []byte
	WebhookKey  []byte
	Settings    map[string]any
}

func (s *Service) load(ctx context.Context, q dbtx.Querier, where string, arg any) (record, error) {
	var r record
	err := q.QueryRow(ctx, `SELECT id, code, adapter, capability, name, enabled, mode, credentials_enc, webhook_secret_enc, settings
		FROM platform.integrations WHERE `+where, arg).
		Scan(&r.ID, &r.Code, &r.Adapter, &r.Capability, &r.Name, &r.Enabled, &r.Mode, &r.Credentials, &r.WebhookKey, &r.Settings)
	return r, err
}

func (s *Service) credentials(r record) (map[string]string, error) {
	out := map[string]string{}
	if len(r.Credentials) == 0 {
		return out, nil
	}
	plain, err := s.Box.Open(r.Credentials)
	if err != nil {
		return nil, fmt.Errorf("integration %s: cannot decrypt credentials: %w", r.Code, err)
	}
	return out, json.Unmarshal(plain, &out)
}

func (s *Service) instantiate(ctx context.Context, r record) (any, error) {
	info, ok := adapterInfo(r.Adapter)
	if !ok {
		return nil, fmt.Errorf("integration %s: unknown adapter %s", r.Code, r.Adapter)
	}
	creds, err := s.credentials(r)
	if err != nil {
		return nil, err
	}
	rid := r.ID
	return info.New(Env{
		Code: r.Code, Mode: r.Mode, Credentials: creds, Settings: r.Settings,
		Log: func(ctx context.Context, c Call) { s.LogCall(ctx, &rid, r.Code, c) },
	})
}

// ErrNotConfigured means no enabled integration provides a capability.
var ErrNotConfigured = errors.New("integration not configured")

// Resolve returns the adapter of the first enabled integration with the
// capability, ordered by code.
func (s *Service) Resolve(ctx context.Context, capability string) (any, string, error) {
	r, err := s.load(ctx, s.DB.Primary, `capability = $1 AND enabled ORDER BY code LIMIT 1`, capability)
	if dbtx.IsNoRows(err) {
		return nil, "", ErrNotConfigured
	}
	if err != nil {
		return nil, "", err
	}
	a, err := s.instantiate(ctx, r)
	return a, r.Code, err
}

// ByCode returns the adapter of one integration (enabled or not).
func (s *Service) ByCode(ctx context.Context, code string) (any, error) {
	r, err := s.load(ctx, s.DB.Primary, `code = $1`, code)
	if err != nil {
		return nil, err
	}
	return s.instantiate(ctx, r)
}

// Payment resolves the payment capability.
func (s *Service) Payment(ctx context.Context) (PaymentAdapter, error) {
	a, code, err := s.Resolve(ctx, CapPayment)
	if err != nil {
		return nil, err
	}
	p, ok := a.(PaymentAdapter)
	if !ok {
		return nil, fmt.Errorf("integration %s does not implement payment", code)
	}
	return p, nil
}

// Messaging resolves the messaging (WhatsApp) capability.
func (s *Service) Messaging(ctx context.Context) (MessagingAdapter, error) {
	a, code, err := s.Resolve(ctx, CapMessaging)
	if err != nil {
		return nil, err
	}
	m, ok := a.(MessagingAdapter)
	if !ok {
		return nil, fmt.Errorf("integration %s does not implement messaging", code)
	}
	return m, nil
}

// Mailer returns the e-mail sender: an enabled e-mail integration, else
// SMTP from environment configuration, else (dev/test) a log-only mailer.
func (s *Service) Mailer(ctx context.Context) (EmailAdapter, error) {
	a, code, err := s.Resolve(ctx, CapEmail)
	if err == nil {
		m, ok := a.(EmailAdapter)
		if !ok {
			return nil, fmt.Errorf("integration %s does not implement email", code)
		}
		return m, nil
	}
	if !errors.Is(err, ErrNotConfigured) {
		return nil, err
	}
	logger := func(ctx context.Context, c Call) { s.LogCall(ctx, nil, "smtp-env", c) }
	if s.Cfg.SMTP.Host != "" {
		return newSMTP(Env{Code: "smtp-env", Mode: "production", Log: logger, Credentials: map[string]string{
			"host": s.Cfg.SMTP.Host, "port": fmt.Sprint(s.Cfg.SMTP.Port), "username": s.Cfg.SMTP.Username,
			"password": s.Cfg.SMTP.Password, "from": s.Cfg.SMTP.From, "tls": fmt.Sprint(s.Cfg.SMTP.TLS),
		}})
	}
	if s.Cfg.Env == "production" {
		return nil, errors.New("no e-mail provider configured")
	}
	return logMailer{log: func(ctx context.Context, c Call) { s.LogCall(ctx, nil, "log-mailer", c) }}, nil
}

// logMailer writes e-mails to the log (development without SMTP).
type logMailer struct{ log CallLogger }

func (m logMailer) SendEmail(ctx context.Context, e Email) error {
	slog.InfoContext(ctx, "email (log mailer)", "to", e.To, "subject", e.Subject)
	m.log(ctx, Call{Operation: "send_email", Request: map[string]any{"to": e.To, "subject": e.Subject, "text": e.Text}})
	return nil
}

// AgentByToken returns a bridge agent id for a token (system scope).
func (s *Service) AgentByToken(ctx context.Context, token string) (uuid.UUID, uuid.UUID, error) {
	var aid, pid uuid.UUID
	err := s.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, property_id FROM platform.bridge_agents WHERE token_hash = $1 AND status = 'active'`,
			secret.HashToken(token)).Scan(&aid, &pid)
	})
	return aid, pid, err
}
