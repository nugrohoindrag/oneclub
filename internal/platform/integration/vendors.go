package integration

// P1 vendor adapters (EP-17): a production payment gateway, the WhatsApp
// Business Cloud API and CAPTCHA for the public website. The vendor choice
// is an open decision (Technical Doc keputusan #3/#4); every adapter sits
// behind the capability interfaces so another vendor is one more file.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// SettlementItem is one settled transaction in a vendor settlement report.
type SettlementItem struct {
	ExternalID string // gateway transaction / invoice id
	Reference  string // OneClub payment number
	Amount     string
	Status     string // settled | pending | failed
	SettledAt  *time.Time
}

// SettlementReporter is implemented by payment adapters that can pull the
// vendor settlement report for the daily reconciliation (FR-PAY-10).
type SettlementReporter interface {
	Settlement(ctx context.Context, from, to time.Time) ([]SettlementItem, error)
}

// CaptchaVerifier is the CAPTCHA capability (FR-WEB-07).
type CaptchaVerifier interface {
	VerifyCaptcha(ctx context.Context, token, remoteIP string) (bool, error)
	SiteKey() string
}

// WebhookChallenger answers a GET verification handshake (Meta webhooks).
type WebhookChallenger interface {
	Challenge(q url.Values) (string, bool)
}

// HTTPClient is used by vendor adapters (replaced in tests).
var HTTPClient = &http.Client{Timeout: 20 * time.Second}

func init() {
	RegisterAdapter(AdapterInfo{
		Key: "xendit", Capability: CapPayment, Name: "Xendit", Webhooks: true,
		Description: "QRIS, Virtual Account and Card through the Xendit hosted invoice page (card data never touches OneClub). " +
			"Set the invoice callback URL to /api/v1/webhooks/<integration code>.",
		Credentials: []Field{
			{Key: "secretKey", Label: "Secret API key", Type: "secret", Required: true},
			{Key: "callbackToken", Label: "Callback verification token", Type: "secret", Required: true,
				Help: "Xendit Dashboard → Settings → Webhooks → verification token"},
		},
		Settings: []Field{
			{Key: "baseUrl", Label: "API base URL", Type: "url", Help: "Default https://api.xendit.co"},
			{Key: "successRedirectUrl", Label: "Success redirect URL", Type: "url"},
			{Key: "failureRedirectUrl", Label: "Failure redirect URL", Type: "url"},
			{Key: "invoiceMinutes", Label: "Invoice validity (minutes)", Type: "number"},
		},
		New: func(env Env) (any, error) { return newXendit(env) },
	})
	RegisterAdapter(AdapterInfo{
		Key: "whatsapp-cloud", Capability: CapMessaging, Name: "WhatsApp Business (Cloud API)", Webhooks: true,
		Description: "WhatsApp Business Platform Cloud API (directly or through a BSP exposing the same API). Business-initiated " +
			"messages use approved templates mapped per event; delivery status arrives through the webhook.",
		Credentials: []Field{
			{Key: "accessToken", Label: "Access token", Type: "secret", Required: true},
			{Key: "phoneNumberId", Label: "Phone number ID", Type: "string", Required: true},
			{Key: "appSecret", Label: "App secret (webhook signature)", Type: "secret", Required: true},
			{Key: "verifyToken", Label: "Webhook verify token", Type: "secret", Required: true},
		},
		Settings: []Field{
			{Key: "baseUrl", Label: "Graph API base URL", Type: "url", Help: "Default https://graph.facebook.com/v21.0"},
			{Key: "templates", Label: "Template map (JSON)", Type: "string",
				Help: `{"golf.booking_confirmed":{"name":"booking_confirmed","params":["name","bookingCode","teeTime"]}}`},
		},
		New: func(env Env) (any, error) { return newWhatsAppCloud(env) },
	})
	RegisterAdapter(AdapterInfo{
		Key: "turnstile", Capability: CapCaptcha, Name: "Cloudflare Turnstile",
		Description: "Bot protection for the public tee time hold and payment (FR-WEB-07).",
		Credentials: []Field{{Key: "secretKey", Label: "Secret key", Type: "secret", Required: true}},
		Settings:    []Field{{Key: "siteKey", Label: "Site key (public)", Type: "string"}},
		New:         func(env Env) (any, error) { return &turnstile{env: env}, nil },
	})
	RegisterAdapter(AdapterInfo{
		Key: "mock-captcha", Capability: CapCaptcha, Name: "Mock CAPTCHA", Sandbox: true,
		Description: "Accepts the token \"pass\" (or every token when Always pass is on).",
		Settings:    []Field{{Key: "alwaysPass", Label: "Always pass", Type: "boolean"}},
		New:         func(env Env) (any, error) { return &mockCaptcha{env: env}, nil },
	})
}

// CapCaptcha is the CAPTCHA capability.
const CapCaptcha = "captcha"

func setting(env Env, key, def string) string {
	if v, ok := env.Settings[key]; ok {
		if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && s != "<nil>" {
			return s
		}
	}
	return def
}

// doJSON performs a JSON call and logs it (masked) through the adapter env.
func doJSON(ctx context.Context, env Env, op, method, u string, hdr http.Header, body any, out any) (int, error) {
	start := time.Now()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := HTTPClient.Do(req)
	code := 0
	var respBody []byte
	if err == nil {
		code = resp.StatusCode
		respBody, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if code >= 300 {
			err = fmt.Errorf("%s: HTTP %d: %s", op, code, truncate(string(respBody), 300))
		} else if out != nil && len(respBody) > 0 {
			err = json.Unmarshal(respBody, out)
		}
	}
	if env.Log != nil {
		var logged any
		_ = json.Unmarshal(respBody, &logged)
		env.Log(ctx, Call{Operation: op, Method: method, URL: u, Request: body, Response: logged, StatusCode: code, Err: err, Duration: time.Since(start)})
	}
	return code, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── Xendit ────────────────────────────────────────────────────────────────

type xendit struct {
	env  Env
	base string
}

func newXendit(env Env) (*xendit, error) {
	if env.Credentials["secretKey"] == "" {
		return nil, errors.New("xendit: secret key is required")
	}
	return &xendit{env: env, base: strings.TrimRight(setting(env, "baseUrl", "https://api.xendit.co"), "/")}, nil
}

func (x *xendit) auth() http.Header {
	h := http.Header{}
	req := &http.Request{Header: h}
	req.SetBasicAuth(x.env.Credentials["secretKey"], "")
	return h
}

var xenditMethods = map[string][]string{
	"qris":            {"QRIS"},
	"virtual_account": {"BCA", "BNI", "BRI", "MANDIRI", "PERMATA"},
	"card":            {"CREDIT_CARD"},
}

type xenditInvoice struct {
	ID          string  `json:"id"`
	ExternalID  string  `json:"external_id"`
	Status      string  `json:"status"`
	Amount      float64 `json:"amount"`
	InvoiceURL  string  `json:"invoice_url"`
	PaidAmount  float64 `json:"paid_amount"`
	PaymentMeth string  `json:"payment_method"`
}

func xenditStatus(s string) string {
	switch strings.ToUpper(s) {
	case "PAID", "SETTLED":
		return "paid"
	case "EXPIRED", "FAILED":
		return "failed"
	}
	return "pending"
}

func (x *xendit) CreatePayment(ctx context.Context, r PaymentRequest) (PaymentResult, error) {
	amt, err := decimal.NewFromString(r.Amount)
	if err != nil {
		return PaymentResult{}, fmt.Errorf("xendit: invalid amount %q", r.Amount)
	}
	minutes, _ := strconv.Atoi(setting(x.env, "invoiceMinutes", "30"))
	body := map[string]any{
		"external_id": r.Reference, "amount": amt.InexactFloat64(), "currency": r.Currency, "description": r.Description,
		"invoice_duration": minutes * 60,
	}
	if m, ok := xenditMethods[r.Method]; ok {
		body["payment_methods"] = m
	}
	if u := setting(x.env, "successRedirectUrl", ""); u != "" {
		body["success_redirect_url"] = u
	}
	if u := setting(x.env, "failureRedirectUrl", ""); u != "" {
		body["failure_redirect_url"] = u
	}
	var inv xenditInvoice
	if _, err := doJSON(ctx, x.env, "create_payment", http.MethodPost, x.base+"/v2/invoices", x.auth(), body, &inv); err != nil {
		return PaymentResult{}, err
	}
	return PaymentResult{ExternalID: inv.ID, Status: xenditStatus(inv.Status), CheckoutURL: inv.InvoiceURL}, nil
}

func (x *xendit) GetPayment(ctx context.Context, externalID string) (PaymentResult, error) {
	var inv xenditInvoice
	if _, err := doJSON(ctx, x.env, "get_payment", http.MethodGet, x.base+"/v2/invoices/"+url.PathEscape(externalID), x.auth(), nil, &inv); err != nil {
		return PaymentResult{}, err
	}
	return PaymentResult{ExternalID: inv.ID, Status: xenditStatus(inv.Status), CheckoutURL: inv.InvoiceURL}, nil
}

func (x *xendit) TestConnection(ctx context.Context) (string, error) {
	var out map[string]any
	if _, err := doJSON(ctx, x.env, "test_connection", http.MethodGet, x.base+"/balance", x.auth(), nil, &out); err != nil {
		return "", err
	}
	return "Connected to Xendit (" + x.env.Mode + ")", nil
}

// VerifyWebhook checks the x-callback-token header (constant time) and maps
// the invoice callback to a payment status event.
func (x *xendit) VerifyWebhook(h http.Header, body []byte, _ string, _ time.Time) (WebhookEvent, error) {
	want := x.env.Credentials["callbackToken"]
	got := h.Get("X-Callback-Token")
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var inv xenditInvoice
	if err := json.Unmarshal(body, &inv); err != nil || inv.ID == "" {
		return WebhookEvent{}, errors.New("xendit: invalid invoice callback")
	}
	amount := inv.PaidAmount
	if amount == 0 {
		amount = inv.Amount
	}
	return WebhookEvent{ExternalID: inv.ID + ":" + strings.ToLower(inv.Status), Type: "payment." + xenditStatus(inv.Status),
		Data: map[string]any{"externalId": inv.ID, "reference": inv.ExternalID, "status": xenditStatus(inv.Status),
			"amount": decimal.NewFromFloat(amount).String(), "method": strings.ToLower(inv.PaymentMeth)}}, nil
}

// RefundPayment refunds an invoice payment through the Xendit refund API.
func (x *xendit) RefundPayment(ctx context.Context, externalID, amount, reason string) (string, error) {
	amt, err := decimal.NewFromString(amount)
	if err != nil {
		return "", fmt.Errorf("xendit: invalid amount %q", amount)
	}
	var out struct {
		ID string `json:"id"`
	}
	if _, err := doJSON(ctx, x.env, "refund_payment", http.MethodPost, x.base+"/refunds", x.auth(),
		map[string]any{"invoice_id": externalID, "amount": amt.InexactFloat64(), "reason": "REQUESTED_BY_CUSTOMER", "metadata": map[string]any{"note": reason}}, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (x *xendit) Settlement(ctx context.Context, from, to time.Time) ([]SettlementItem, error) {
	var out []SettlementItem
	after := ""
	for page := 0; page < 50; page++ {
		q := url.Values{"types": {"PAYMENT"}, "created[gte]": {from.UTC().Format(time.RFC3339)}, "created[lt]": {to.UTC().Format(time.RFC3339)}, "limit": {"50"}}
		if after != "" {
			q.Set("after_id", after)
		}
		var resp struct {
			HasMore bool `json:"has_more"`
			Data    []struct {
				ID               string  `json:"id"`
				ProductID        string  `json:"product_id"`
				ReferenceID      string  `json:"reference_id"`
				Amount           float64 `json:"amount"`
				Status           string  `json:"status"`
				SettlementStatus string  `json:"settlement_status"`
				Updated          string  `json:"updated"`
			} `json:"data"`
		}
		if _, err := doJSON(ctx, x.env, "settlement_report", http.MethodGet, x.base+"/transactions?"+q.Encode(), x.auth(), nil, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			st := "pending"
			if strings.EqualFold(d.SettlementStatus, "SETTLED") || strings.EqualFold(d.Status, "SUCCESS") {
				st = "settled"
			}
			if strings.EqualFold(d.Status, "FAILED") || strings.EqualFold(d.Status, "VOIDED") {
				st = "failed"
			}
			var at *time.Time
			if t, err := time.Parse(time.RFC3339, d.Updated); err == nil {
				at = &t
			}
			ext := d.ProductID
			if ext == "" {
				ext = d.ID
			}
			out = append(out, SettlementItem{ExternalID: ext, Reference: d.ReferenceID, Amount: decimal.NewFromFloat(d.Amount).String(), Status: st, SettledAt: at})
			after = d.ID
		}
		if !resp.HasMore {
			break
		}
	}
	return out, nil
}

// ── WhatsApp Business Cloud API ───────────────────────────────────────────

type waTemplate struct {
	Name     string   `json:"name"`
	Language string   `json:"language"`
	Params   []string `json:"params"`
}

type whatsAppCloud struct {
	env       Env
	base      string
	templates map[string]waTemplate
}

func newWhatsAppCloud(env Env) (*whatsAppCloud, error) {
	w := &whatsAppCloud{env: env, base: strings.TrimRight(setting(env, "baseUrl", "https://graph.facebook.com/v21.0"), "/"), templates: map[string]waTemplate{}}
	if raw := setting(env, "templates", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &w.templates); err != nil {
			return nil, fmt.Errorf("whatsapp: template map is not valid JSON: %w", err)
		}
	}
	return w, nil
}

// normalizePhone converts 08xx / +62 / 62 to the digits-only E.164 form.
func normalizePhone(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if strings.HasPrefix(s, "0") {
		s = "62" + s[1:]
	}
	return s
}

func (w *whatsAppCloud) SendMessage(ctx context.Context, m OutboundMessage) (MessageResult, error) {
	to := normalizePhone(m.To)
	if to == "" {
		return MessageResult{}, errors.New("whatsapp: recipient phone is required")
	}
	body := map[string]any{"messaging_product": "whatsapp", "to": to}
	if t, ok := w.templates[m.Template]; ok {
		lang := t.Language
		if lang == "" {
			lang = map[string]string{"id": "id", "en": "en_US"}[m.Language]
		}
		var params []map[string]any
		for _, k := range t.Params {
			params = append(params, map[string]any{"type": "text", "text": m.Named[k]})
		}
		tpl := map[string]any{"name": t.Name, "language": map[string]any{"code": lang}}
		if len(params) > 0 {
			tpl["components"] = []map[string]any{{"type": "body", "parameters": params}}
		}
		body["type"] = "template"
		body["template"] = tpl
	} else {
		body["type"] = "text"
		body["text"] = map[string]any{"body": m.Text, "preview_url": true}
	}
	hdr := http.Header{"Authorization": {"Bearer " + w.env.Credentials["accessToken"]}}
	var resp struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if _, err := doJSON(ctx, w.env, "send_message", http.MethodPost, w.base+"/"+url.PathEscape(w.env.Credentials["phoneNumberId"])+"/messages", hdr, body, &resp); err != nil {
		return MessageResult{}, err
	}
	out := MessageResult{Status: "sent"}
	if len(resp.Messages) > 0 {
		out.ExternalID = resp.Messages[0].ID
	}
	return out, nil
}

func (w *whatsAppCloud) TestConnection(ctx context.Context) (string, error) {
	hdr := http.Header{"Authorization": {"Bearer " + w.env.Credentials["accessToken"]}}
	var out map[string]any
	if _, err := doJSON(ctx, w.env, "test_connection", http.MethodGet, w.base+"/"+url.PathEscape(w.env.Credentials["phoneNumberId"]), hdr, nil, &out); err != nil {
		return "", err
	}
	return fmt.Sprintf("WhatsApp number %v reachable", out["display_phone_number"]), nil
}

// Challenge answers Meta's subscription handshake.
func (w *whatsAppCloud) Challenge(q url.Values) (string, bool) {
	tok := w.env.Credentials["verifyToken"]
	if q.Get("hub.mode") == "subscribe" && tok != "" && subtle.ConstantTimeCompare([]byte(q.Get("hub.verify_token")), []byte(tok)) == 1 {
		return q.Get("hub.challenge"), true
	}
	return "", false
}

// VerifyWebhook checks X-Hub-Signature-256 and returns the delivery
// statuses (sent / delivered / read / failed) of outbound messages.
func (w *whatsAppCloud) VerifyWebhook(h http.Header, body []byte, _ string, _ time.Time) (WebhookEvent, error) {
	sig := strings.TrimPrefix(h.Get("X-Hub-Signature-256"), "sha256=")
	mac := hmac.New(sha256.New, []byte(w.env.Credentials["appSecret"]))
	mac.Write(body)
	want := mac.Sum(nil)
	got, err := hex.DecodeString(sig)
	if err != nil || w.env.Credentials["appSecret"] == "" || !hmac.Equal(want, got) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var p struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Statuses []struct {
						ID     string `json:"id"`
						Status string `json:"status"`
					} `json:"statuses"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return WebhookEvent{}, errors.New("whatsapp: invalid webhook body")
	}
	var statuses []any
	for _, e := range p.Entry {
		for _, c := range e.Changes {
			for _, s := range c.Value.Statuses {
				statuses = append(statuses, map[string]any{"messageId": s.ID, "status": s.Status})
			}
		}
	}
	sum := sha256.Sum256(body)
	return WebhookEvent{ExternalID: "wa:" + hex.EncodeToString(sum[:16]), Type: "message.status", Data: map[string]any{"statuses": statuses}}, nil
}

// ── CAPTCHA ───────────────────────────────────────────────────────────────

type turnstile struct{ env Env }

func (t *turnstile) SiteKey() string { return setting(t.env, "siteKey", "") }

func (t *turnstile) VerifyCaptcha(ctx context.Context, token, remoteIP string) (bool, error) {
	start := time.Now()
	form := url.Values{"secret": {t.env.Credentials["secretKey"]}, "response": {token}, "remoteip": {remoteIP}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := HTTPClient.Do(req)
	var out struct {
		Success bool     `json:"success"`
		Errors  []string `json:"error-codes"`
	}
	code := 0
	if err == nil {
		code = resp.StatusCode
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
	}
	if t.env.Log != nil {
		t.env.Log(ctx, Call{Operation: "verify_captcha", Method: http.MethodPost, URL: "turnstile/siteverify", Response: out, StatusCode: code, Err: err, Duration: time.Since(start)})
	}
	return err == nil && out.Success, err
}

func (t *turnstile) TestConnection(context.Context) (string, error) {
	if t.env.Credentials["secretKey"] == "" {
		return "", errors.New("secret key missing")
	}
	return "Turnstile configured", nil
}

type mockCaptcha struct{ env Env }

func (m *mockCaptcha) SiteKey() string { return "mock-site-key" }

func (m *mockCaptcha) VerifyCaptcha(ctx context.Context, token, _ string) (bool, error) {
	ok := token == "pass"
	if v, _ := m.env.Settings["alwaysPass"].(bool); v {
		ok = true
	}
	return ok, timed(m.env, ctx, "verify_captcha", map[string]any{"token": token}, func() (any, int, error) { return map[string]any{"success": ok}, 200, nil })
}

func (m *mockCaptcha) TestConnection(context.Context) (string, error) {
	return "Mock CAPTCHA ready", nil
}

// Captcha resolves the CAPTCHA capability; ErrNotConfigured when none.
func (s *Service) Captcha(ctx context.Context) (CaptchaVerifier, error) {
	a, code, err := s.Resolve(ctx, CapCaptcha)
	if err != nil {
		return nil, err
	}
	c, ok := a.(CaptchaVerifier)
	if !ok {
		return nil, fmt.Errorf("integration %s does not implement captcha", code)
	}
	return c, nil
}

// PaymentWithCode resolves the payment capability and its integration code.
func (s *Service) PaymentWithCode(ctx context.Context) (PaymentAdapter, string, error) {
	a, code, err := s.Resolve(ctx, CapPayment)
	if err != nil {
		return nil, "", err
	}
	p, ok := a.(PaymentAdapter)
	if !ok {
		return nil, "", fmt.Errorf("integration %s does not implement payment", code)
	}
	return p, code, nil
}

// SettlementFor returns the settlement reporter of an integration (nil when
// the adapter has none — the settlement file is then uploaded).
func (s *Service) SettlementFor(ctx context.Context, code string) (SettlementReporter, error) {
	a, err := s.ByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	r, _ := a.(SettlementReporter)
	return r, nil
}
