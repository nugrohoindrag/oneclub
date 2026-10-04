package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneclub/internal/kernel/id"
)

func init() {
	RegisterAdapter(AdapterInfo{
		Key: "mock-payment", Capability: CapPayment, Name: "Mock Payment Gateway", Sandbox: true, Webhooks: true,
		Description: "Sandbox payment gateway for development (QRIS, Virtual Account, Card). Webhooks are signed with HMAC-SHA256.",
		Settings:    []Field{{Key: "autoPay", Label: "Mark payments paid immediately", Type: "boolean"}},
		New:         func(env Env) (any, error) { return &mockPayment{env: env}, nil },
	})
	RegisterAdapter(AdapterInfo{
		Key: "mock-whatsapp", Capability: CapMessaging, Name: "Mock WhatsApp Business", Sandbox: true, Webhooks: true,
		Description: "Sandbox WhatsApp messaging until the BSP is selected (P1). Inbound messages arrive as webhooks signed with HMAC-SHA256.",
		New:         func(env Env) (any, error) { return &mockMessaging{env: env}, nil },
	})
	RegisterAdapter(AdapterInfo{
		Key: "mock-email", Capability: CapEmail, Name: "Mock E-mail", Sandbox: true,
		Description: "Captures e-mails in the integration log. Can simulate temporary failures.",
		Settings: []Field{
			{Key: "failTimes", Label: "Fail the first N sends (testing retries)", Type: "number"},
			{Key: "alwaysFail", Label: "Always fail", Type: "boolean"},
		},
		New: func(env Env) (any, error) { return &mockEmail{env: env}, nil },
	})
	RegisterAdapter(AdapterInfo{
		Key: "smtp", Capability: CapEmail, Name: "SMTP",
		Description: "Any SMTP provider (STARTTLS on 587, implicit TLS on 465).",
		Credentials: []Field{
			{Key: "host", Label: "Host", Type: "string", Required: true},
			{Key: "port", Label: "Port", Type: "number", Required: true},
			{Key: "username", Label: "Username", Type: "string"},
			{Key: "password", Label: "Password", Type: "secret"},
			{Key: "from", Label: "From address", Type: "string", Required: true},
			{Key: "tls", Label: "Implicit TLS (port 465)", Type: "boolean"},
		},
		New: func(env Env) (any, error) { return newSMTP(env) },
	})
	RegisterAdapter(AdapterInfo{
		Key: "mock-efaktur", Capability: CapTaxInvoice, Name: "Mock e-Faktur (Coretax)", Sandbox: true,
		Description: "Sandbox tax invoice submission.",
		New:         func(env Env) (any, error) { return &mockTaxInvoice{env: env}, nil },
	})
	RegisterAdapter(AdapterInfo{
		Key: "mock-resident", Capability: CapResidentData, Name: "Mock Modernland Resident Data", Sandbox: true,
		Description: "Sandbox resident lookup for residence rate verification.",
		New:         func(env Env) (any, error) { return &mockResident{env: env}, nil },
	})
	RegisterAdapter(AdapterInfo{
		Key: "bridge-agent", Capability: CapHardware, Name: "Local Bridge Agent",
		Description: "Hardware (locker, turnstile, ball dispenser) through the OneClub bridge agent in the club network.",
		New:         func(env Env) (any, error) { return &bridgeHardware{env: env}, nil },
	})
}

func timed(env Env, ctx context.Context, op string, req any, fn func() (any, int, error)) error {
	start := time.Now()
	resp, code, err := fn()
	if env.Log != nil {
		env.Log(ctx, Call{Operation: op, Request: req, Response: resp, StatusCode: code, Err: err, Duration: time.Since(start)})
	}
	return err
}

// ── Mock payment (FR-INT-05) ──────────────────────────────────────────────

type mockPayment struct{ env Env }

func (m *mockPayment) CreatePayment(ctx context.Context, r PaymentRequest) (PaymentResult, error) {
	var out PaymentResult
	err := timed(m.env, ctx, "create_payment", r, func() (any, int, error) {
		if r.Amount == "" || r.Reference == "" {
			return nil, 400, errors.New("amount and reference are required")
		}
		out = PaymentResult{ExternalID: "mockpay_" + id.New().String(), Status: "pending",
			CheckoutURL: "https://sandbox.pay.local/checkout/" + r.Reference}
		switch r.Method {
		case "qris":
			out.QRString = "00020101021226670016ID.MOCK.QRIS0118" + r.Reference
		case "virtual_account":
			out.VANumber = "8808" + strconv.FormatInt(time.Now().UnixNano()%1e10, 10)
		}
		if v, _ := m.env.Settings["autoPay"].(bool); v {
			out.Status = "paid"
		}
		return out, 201, nil
	})
	return out, err
}

func (m *mockPayment) GetPayment(ctx context.Context, externalID string) (PaymentResult, error) {
	out := PaymentResult{ExternalID: externalID, Status: "pending"}
	err := timed(m.env, ctx, "get_payment", map[string]any{"externalId": externalID}, func() (any, int, error) { return out, 200, nil })
	return out, err
}

// RefundPayment refunds through the sandbox gateway.
func (m *mockPayment) RefundPayment(ctx context.Context, externalID, amount, reason string) (string, error) {
	out := "mockrefund_" + id.New().String()
	return out, timed(m.env, ctx, "refund_payment", map[string]any{"externalId": externalID, "amount": amount, "reason": reason},
		func() (any, int, error) { return map[string]any{"refundId": out}, 201, nil })
}

func (m *mockPayment) TestConnection(ctx context.Context) (string, error) {
	err := timed(m.env, ctx, "test_connection", nil, func() (any, int, error) { return map[string]any{"ok": true}, 200, nil })
	return "Mock payment gateway reachable (" + m.env.Mode + ")", err
}

// SignatureHeader is the webhook signature header for mock adapters:
// "t=<unix seconds>,v1=<hex HMAC-SHA256(secret, t + "." + body)>".
const SignatureHeader = "X-OneClub-Signature"

// Sign produces a SignatureHeader value (used by tests and the sandbox).
func Sign(secret string, body []byte, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyHMAC checks a SignatureHeader with a 5 minute tolerance.
func VerifyHMAC(header, secret string, body []byte, now time.Time) error {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	if ts == "" || sig == "" || secret == "" {
		return ErrInvalidSignature
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrInvalidSignature
	}
	if d := now.Sub(time.Unix(unix, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return fmt.Errorf("%w: timestamp outside tolerance", ErrInvalidSignature)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	got, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(want, got) {
		return ErrInvalidSignature
	}
	return nil
}

func (m *mockPayment) VerifyWebhook(h http.Header, body []byte, secret string, now time.Time) (WebhookEvent, error) {
	return verifyMockWebhook(h, body, secret, now)
}

// verifyMockWebhook checks the HMAC signature of a sandbox webhook whose
// body is {id, type, data}.
func verifyMockWebhook(h http.Header, body []byte, secret string, now time.Time) (WebhookEvent, error) {
	if err := VerifyHMAC(h.Get(SignatureHeader), secret, body, now); err != nil {
		return WebhookEvent{}, err
	}
	var p struct {
		ID   string         `json:"id"`
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.ID == "" || p.Type == "" {
		return WebhookEvent{}, errors.New("webhook body must contain id and type")
	}
	return WebhookEvent{ExternalID: p.ID, Type: p.Type, Data: p.Data}, nil
}

// ── Mock messaging ────────────────────────────────────────────────────────

type mockMessaging struct{ env Env }

// VerifyWebhook accepts signed inbound messages: the sandbox of the BSP
// webhook ({id, type: "message.received", data: {messages: [{id, from,
// name, text}]}}), e.g. WhatsApp inquiries becoming leads (PRD P3 FR-INT-P3-01).
func (m *mockMessaging) VerifyWebhook(h http.Header, body []byte, secret string, now time.Time) (WebhookEvent, error) {
	return verifyMockWebhook(h, body, secret, now)
}

func (m *mockMessaging) SendMessage(ctx context.Context, msg OutboundMessage) (MessageResult, error) {
	out := MessageResult{ExternalID: "wamid.mock." + id.New().String(), Status: "sent"}
	err := timed(m.env, ctx, "send_message", map[string]any{"phone": msg.To, "template": msg.Template, "language": msg.Language}, func() (any, int, error) {
		if msg.To == "" {
			return nil, 400, errors.New("recipient phone is required")
		}
		return out, 200, nil
	})
	return out, err
}

func (m *mockMessaging) TestConnection(ctx context.Context) (string, error) {
	return "Mock WhatsApp reachable", timed(m.env, ctx, "test_connection", nil, func() (any, int, error) { return map[string]any{"ok": true}, 200, nil })
}

// ── Mock e-mail ───────────────────────────────────────────────────────────

var (
	mockMailMu    sync.Mutex
	mockMailCount = map[string]int{}
)

type mockEmail struct{ env Env }

func num(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

func (m *mockEmail) SendEmail(ctx context.Context, e Email) error {
	return timed(m.env, ctx, "send_email", map[string]any{"email": e.To, "subject": e.Subject, "text": e.Text}, func() (any, int, error) {
		if v, _ := m.env.Settings["alwaysFail"].(bool); v {
			return nil, 451, errors.New("simulated permanent SMTP failure")
		}
		if n := num(m.env.Settings["failTimes"]); n > 0 {
			mockMailMu.Lock()
			mockMailCount[m.env.Code]++
			c := mockMailCount[m.env.Code]
			mockMailMu.Unlock()
			if c <= n {
				return nil, 421, fmt.Errorf("simulated temporary SMTP failure %d/%d", c, n)
			}
		}
		return map[string]any{"messageId": id.New().String()}, 250, nil
	})
}

func (m *mockEmail) TestConnection(ctx context.Context) (string, error) {
	return "Mock e-mail ready", timed(m.env, ctx, "test_connection", nil, func() (any, int, error) { return map[string]any{"ok": true}, 250, nil })
}

// ResetMockEmail clears simulated failure counters (tests).
func ResetMockEmail() {
	mockMailMu.Lock()
	mockMailCount = map[string]int{}
	mockMailMu.Unlock()
}

// ── SMTP ──────────────────────────────────────────────────────────────────

type smtpMailer struct {
	env              Env
	host, user, pass string
	port             int
	from             string
	implicitTLS      bool
}

func newSMTP(env Env) (*smtpMailer, error) {
	c := env.Credentials
	port, _ := strconv.Atoi(c["port"])
	if port == 0 {
		port = 587
	}
	if c["host"] == "" || c["from"] == "" {
		return nil, errors.New("smtp: host and from are required")
	}
	return &smtpMailer{env: env, host: c["host"], port: port, user: c["username"], pass: c["password"], from: c["from"],
		implicitTLS: c["tls"] == "true" || port == 465}, nil
}

func (m *smtpMailer) client() (*smtp.Client, error) {
	addr := net.JoinHostPort(m.host, strconv.Itoa(m.port))
	var conn net.Conn
	var err error
	d := &net.Dialer{Timeout: 15 * time.Second}
	if m.implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !m.implicitTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}); err != nil {
				c.Close()
				return nil, err
			}
		}
	}
	if m.user != "" {
		if err := c.Auth(smtp.PlainAuth("", m.user, m.pass, m.host)); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

func (m *smtpMailer) SendEmail(ctx context.Context, e Email) error {
	return timed(m.env, ctx, "send_email", map[string]any{"email": e.To, "subject": e.Subject}, func() (any, int, error) {
		fromAddr := m.from
		if i := strings.LastIndex(fromAddr, "<"); i >= 0 {
			fromAddr = strings.Trim(fromAddr[i:], "<>")
		}
		msg, err := buildMIME(m.from, e)
		if err != nil {
			return nil, 0, err
		}
		c, err := m.client()
		if err != nil {
			return nil, 0, err
		}
		defer c.Close()
		if err := c.Mail(fromAddr); err != nil {
			return nil, 0, err
		}
		if err := c.Rcpt(e.To); err != nil {
			return nil, 0, err
		}
		wc, err := c.Data()
		if err != nil {
			return nil, 0, err
		}
		if _, err := wc.Write(msg); err != nil {
			return nil, 0, err
		}
		if err := wc.Close(); err != nil {
			return nil, 0, err
		}
		return map[string]any{"accepted": true}, 250, c.Quit()
	})
}

func (m *smtpMailer) TestConnection(ctx context.Context) (string, error) {
	err := timed(m.env, ctx, "test_connection", map[string]any{"host": m.host, "port": m.port}, func() (any, int, error) {
		c, err := m.client()
		if err != nil {
			return nil, 0, err
		}
		defer c.Close()
		return map[string]any{"connected": true}, 250, c.Quit()
	})
	if err != nil {
		return "", err
	}
	return "Connected to " + m.host, nil
}

// buildMIME renders a multipart/alternative (text + HTML) message.
func buildMIME(from string, e Email) ([]byte, error) {
	if e.FromName != "" {
		addr := "<" + from + ">"
		if i := strings.LastIndex(from, "<"); i >= 0 {
			addr = from[i:]
		}
		from = mime.QEncoding.Encode("utf-8", e.FromName) + " " + addr
	}
	to := e.To
	if e.ToName != "" {
		to = mime.QEncoding.Encode("utf-8", e.ToName) + " <" + e.To + ">"
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, part := range []struct{ ctype, content string }{{"text/plain; charset=utf-8", e.Text}, {"text/html; charset=utf-8", e.HTML}} {
		if part.content == "" {
			continue
		}
		w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {part.ctype}, "Content-Transfer-Encoding": {"8bit"}})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(part.content)); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	header := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%s\r\n\r\n",
		from, to, mime.QEncoding.Encode("utf-8", e.Subject), time.Now().Format(time.RFC1123Z), mw.Boundary())
	return append([]byte(header), body.Bytes()...), nil
}

// ── Mock e-Faktur & resident data ────────────────────────────────────────

type mockTaxInvoice struct{ env Env }

func (m *mockTaxInvoice) SubmitInvoice(ctx context.Context, inv TaxInvoice) (TaxInvoiceResult, error) {
	out := TaxInvoiceResult{ExternalID: id.New().String(), Number: "010.000-26." + strconv.FormatInt(time.Now().Unix()%1e8, 10), Status: "approved"}
	return out, timed(m.env, ctx, "submit_invoice", inv, func() (any, int, error) { return out, 200, nil })
}

func (m *mockTaxInvoice) TestConnection(ctx context.Context) (string, error) {
	return "Mock e-Faktur reachable", timed(m.env, ctx, "test_connection", nil, func() (any, int, error) { return map[string]any{"ok": true}, 200, nil })
}

type mockResident struct{ env Env }

func (m *mockResident) LookupResident(ctx context.Context, q string) ([]Resident, error) {
	out := []Resident{{ResidentID: "RES-0001", Name: "Sample Resident", Address: "Modernland Cluster A-1", Status: "active"}}
	return out, timed(m.env, ctx, "lookup_resident", map[string]any{"query": q}, func() (any, int, error) { return map[string]any{"count": len(out)}, 200, nil })
}

func (m *mockResident) TestConnection(ctx context.Context) (string, error) {
	return "Mock resident data reachable", timed(m.env, ctx, "test_connection", nil, func() (any, int, error) { return map[string]any{"ok": true}, 200, nil })
}

// ── Hardware via bridge agent (FR-INT-07) ────────────────────────────────

type bridgeHardware struct{ env Env }

// SendCommand is a P0 skeleton: commands are logged; the agent command
// channel (long-poll on heartbeat) is delivered with the first hardware
// integration in P2.
func (b *bridgeHardware) SendCommand(ctx context.Context, c HardwareCommand) error {
	return timed(b.env, ctx, "send_command", c, func() (any, int, error) {
		return map[string]any{"queued": true}, 202, nil
	})
}
