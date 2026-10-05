package integration

// Production e-mail (PRD P4 FR-INT-P4-05): notifications, P3 campaigns and
// RFQ / PO documents leave from the club's own domain. Two production
// options sit behind the e-mail capability: the existing "smtp" adapter
// (any provider's SMTP relay) and "sendgrid" (Twilio SendGrid v3 HTTP API,
// no SMTP port needed in the data centre). Deliverability depends on the
// DNS of the sender domain, so Settings → Integrations checks it: one SPF
// record authorising the provider, a DKIM key per selector and a DMARC
// policy (CheckEmailDomain). The recipient address is masked in the
// integration log (FR-INT-P4-08); message bodies are never logged.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"time"
)

func init() {
	RegisterAdapter(AdapterInfo{
		Key: "sendgrid", Capability: CapEmail, Name: "SendGrid (HTTP API)",
		Description: "Transactional and campaign e-mail through the Twilio SendGrid v3 API from the club's domain. Authenticate the domain at the " +
			"provider (SPF include and DKIM CNAMEs) and run the domain check before switching to production.",
		Credentials: []Field{{Key: "apiKey", Label: "API key", Type: "secret", Required: true}},
		Settings: []Field{
			{Key: "from", Label: "From address", Type: "string", Required: true, Help: "e.g. no-reply@moderngolf.co.id (on the authenticated domain)"},
			{Key: "fromName", Label: "From name", Type: "string"},
			{Key: "replyTo", Label: "Reply-to address", Type: "string"},
			{Key: "baseUrl", Label: "API base URL", Type: "url", Help: "Default https://api.sendgrid.com"},
			{Key: "dkimSelectors", Label: "DKIM selectors (comma-separated)", Type: "string", Help: "Default s1,s2"},
			{Key: "spfInclude", Label: "SPF include", Type: "string", Help: "Default sendgrid.net"},
		},
		New: func(env Env) (any, error) { return newSendGrid(env) },
	})
}

// ── SendGrid ──────────────────────────────────────────────────────────────

type sendGrid struct {
	env  Env
	base string
	from mail.Address
}

func newSendGrid(env Env) (*sendGrid, error) {
	if env.Credentials["apiKey"] == "" {
		return nil, errors.New("sendgrid: API key is required")
	}
	from, err := mail.ParseAddress(setting(env, "from", ""))
	if err != nil {
		return nil, errors.New("sendgrid: a valid from address is required")
	}
	from.Name = setting(env, "fromName", from.Name)
	return &sendGrid{env: env, base: strings.TrimRight(setting(env, "baseUrl", "https://api.sendgrid.com"), "/"), from: *from}, nil
}

func (s *sendGrid) auth() http.Header {
	return http.Header{"Authorization": {"Bearer " + s.env.Credentials["apiKey"]}}
}

type sgAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type sgContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type sgMail struct {
	Personalizations []struct {
		To []sgAddress `json:"to"`
	} `json:"personalizations"`
	From     sgAddress   `json:"from"`
	ReplyTo  *sgAddress  `json:"reply_to,omitempty"`
	Subject  string      `json:"subject"`
	Content  []sgContent `json:"content"`
	Settings *struct {
		Sandbox struct {
			Enable bool `json:"enable"`
		} `json:"sandbox_mode"`
	} `json:"mail_settings,omitempty"`
}

// SendEmail posts the message to /v3/mail/send (202 Accepted).
func (s *sendGrid) SendEmail(ctx context.Context, e Email) error {
	to, err := mail.ParseAddress(e.To)
	if err != nil {
		return fmt.Errorf("sendgrid: invalid recipient: %w", err)
	}
	m := sgMail{From: sgAddress{Email: s.from.Address, Name: s.from.Name}, Subject: e.Subject}
	if e.FromName != "" {
		m.From.Name = e.FromName
	}
	m.Personalizations = make([]struct {
		To []sgAddress `json:"to"`
	}, 1)
	m.Personalizations[0].To = []sgAddress{{Email: to.Address, Name: e.ToName}}
	if r := setting(s.env, "replyTo", ""); r != "" {
		m.ReplyTo = &sgAddress{Email: r}
	}
	if e.Text != "" || e.HTML == "" {
		m.Content = append(m.Content, sgContent{Type: "text/plain", Value: e.Text})
	}
	if e.HTML != "" {
		m.Content = append(m.Content, sgContent{Type: "text/html", Value: e.HTML})
	}
	if s.env.Mode == "sandbox" {
		// SendGrid validates the request but delivers nothing.
		m.Settings = &struct {
			Sandbox struct {
				Enable bool `json:"enable"`
			} `json:"sandbox_mode"`
		}{}
		m.Settings.Sandbox.Enable = true
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// Logged: the masked recipient and the subject — never the body.
	_, err = p4Call(ctx, s.env, "send_email", http.MethodPost, s.base+"/v3/mail/send", s.auth(), body,
		map[string]any{"email": to.Address, "subject": e.Subject, "sandbox": s.env.Mode == "sandbox"}, nil, nil)
	return err
}

// TestConnection reads the API key scopes (no e-mail is sent).
func (s *sendGrid) TestConnection(ctx context.Context) (string, error) {
	var out struct {
		Scopes []string `json:"scopes"`
	}
	if _, err := p4Call(ctx, s.env, "test_connection", http.MethodGet, s.base+"/v3/scopes", s.auth(), nil, nil, countOnly("scopes"), &out); err != nil {
		return "", err
	}
	if !slices.Contains(out.Scopes, "mail.send") {
		return "", errors.New("sendgrid: the API key has no mail.send permission")
	}
	return "SendGrid reachable, the key may send mail (" + s.env.Mode + ")", nil
}

// ── sender domain check (SPF, DKIM, DMARC) ────────────────────────────────

// LookupTXT resolves DNS TXT records (replaced in tests: no network).
var LookupTXT = net.DefaultResolver.LookupTXT

// EmailDomainCheckInput names the domain and the provider's records; empty
// values are taken from the integration (from address, settings).
type EmailDomainCheckInput struct {
	Domain        string   `json:"domain,omitempty" doc:"Sender domain (default: the domain of the from address)"`
	DKIMSelectors []string `json:"dkimSelectors,omitempty" doc:"DKIM selectors (default: settings dkimSelectors, or s1,s2 for SendGrid)"`
	SPFInclude    string   `json:"spfInclude,omitempty" doc:"Mechanism the SPF record must include (default: settings spfInclude)"`
}

// DNSRecordCheck is the result for one record.
type DNSRecordCheck struct {
	Record  string `json:"record" enum:"spf,dkim,dmarc"`
	Name    string `json:"name" doc:"DNS name queried"`
	Status  string `json:"status" enum:"pass,warning,missing,invalid"`
	Value   string `json:"value,omitempty"`
	Message string `json:"message"`
}

// EmailDomainReport is the deliverability check of a sender domain.
type EmailDomainReport struct {
	IntegrationCode string           `json:"integrationCode"`
	Domain          string           `json:"domain"`
	OK              bool             `json:"ok" doc:"SPF, every DKIM selector and DMARC pass (warnings allowed)"`
	Checks          []DNSRecordCheck `json:"checks"`
	CheckedAt       time.Time        `json:"checkedAt"`
}

func isDomainName(s string) bool {
	if len(s) < 3 || len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func txt(ctx context.Context, name string) ([]string, error) {
	recs, err := LookupTXT(ctx, name)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, nil
	}
	return recs, err
}

// CheckEmailDomain checks SPF, DKIM (every selector) and DMARC of a domain.
func CheckEmailDomain(ctx context.Context, domain string, selectors []string, spfInclude string) ([]DNSRecordCheck, error) {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if !isDomainName(domain) {
		return nil, fmt.Errorf("%q is not a domain name", domain)
	}
	var out []DNSRecordCheck
	// SPF: exactly one v=spf1 record, including the provider, ending in -all / ~all.
	spf := DNSRecordCheck{Record: "spf", Name: domain}
	recs, err := txt(ctx, domain)
	if err != nil {
		return nil, err
	}
	var spfs []string
	for _, r := range recs {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r)), "v=spf1") {
			spfs = append(spfs, strings.TrimSpace(r))
		}
	}
	switch {
	case len(spfs) == 0:
		spf.Status, spf.Message = "missing", "no SPF record (TXT v=spf1 …) on the domain"
	case len(spfs) > 1:
		spf.Status, spf.Value, spf.Message = "invalid", strings.Join(spfs, " | "), "more than one SPF record: receivers treat SPF as failed"
	default:
		spf.Value = spfs[0]
		terms := strings.Fields(strings.ToLower(spfs[0]))
		switch {
		case spfInclude != "" && !slices.Contains(terms, "include:"+strings.ToLower(spfInclude)):
			spf.Status, spf.Message = "invalid", "the SPF record does not include "+spfInclude
		case slices.Contains(terms, "+all") || slices.Contains(terms, "all"):
			spf.Status, spf.Message = "invalid", "the SPF record allows every server (+all)"
		case !slices.Contains(terms, "-all") && !slices.Contains(terms, "~all"):
			spf.Status, spf.Message = "warning", "the SPF record should end with -all or ~all"
		default:
			spf.Status, spf.Message = "pass", "SPF authorises the provider"
		}
	}
	out = append(out, spf)
	// DKIM: a public key per selector.
	for _, sel := range selectors {
		sel = strings.ToLower(strings.TrimSpace(sel))
		if sel == "" {
			continue
		}
		c := DNSRecordCheck{Record: "dkim", Name: sel + "._domainkey." + domain}
		recs, err := txt(ctx, c.Name)
		if err != nil {
			return nil, err
		}
		val := strings.Join(recs, "")
		tags := dnsTags(val)
		switch {
		case val == "":
			c.Status, c.Message = "missing", "no DKIM key for selector "+sel
		case tags["v"] != "" && !strings.EqualFold(tags["v"], "DKIM1"):
			c.Status, c.Value, c.Message = "invalid", val, "not a DKIM1 record"
		case tags["p"] == "":
			c.Status, c.Value, c.Message = "invalid", val, "the DKIM key is empty (revoked)"
		default:
			c.Status, c.Value, c.Message = "pass", truncate(val, 120), "DKIM key published"
		}
		out = append(out, c)
	}
	// DMARC: a policy record; p=none only monitors.
	dm := DNSRecordCheck{Record: "dmarc", Name: "_dmarc." + domain}
	recs, err = txt(ctx, dm.Name)
	if err != nil {
		return nil, err
	}
	var dmarc string
	for _, r := range recs {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(r)), "V=DMARC1") {
			dmarc = strings.TrimSpace(r)
		}
	}
	tags := dnsTags(dmarc)
	switch {
	case dmarc == "":
		dm.Status, dm.Message = "missing", "no DMARC record (TXT v=DMARC1; p=…)"
	case tags["p"] == "":
		dm.Status, dm.Value, dm.Message = "invalid", dmarc, "the DMARC record has no policy (p=)"
	case strings.EqualFold(tags["p"], "none"):
		dm.Status, dm.Value, dm.Message = "warning", dmarc, "DMARC only monitors (p=none); move to quarantine or reject once reports are clean"
	default:
		dm.Status, dm.Value, dm.Message = "pass", dmarc, "DMARC policy "+tags["p"]
	}
	out = append(out, dm)
	return out, nil
}

// dnsTags parses "k=v; k=v" DKIM / DMARC records.
func dnsTags(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return out
}

// emailDomainDefaults derives the domain, selectors and SPF include of an
// e-mail integration (from address in settings or credentials).
func emailDomainDefaults(adapter string, settings map[string]any, creds map[string]string, in EmailDomainCheckInput) (string, []string, string) {
	env := Env{Settings: settings}
	domain := strings.TrimSpace(in.Domain)
	if domain == "" {
		from := setting(env, "from", creds["from"])
		if a, err := mail.ParseAddress(from); err == nil {
			_, domain, _ = strings.Cut(a.Address, "@")
		}
	}
	sels := in.DKIMSelectors
	if len(sels) == 0 {
		sels = listSetting(settings, "dkimSelectors")
	}
	include := strings.TrimSpace(in.SPFInclude)
	if include == "" {
		include = setting(env, "spfInclude", "")
	}
	if adapter == "sendgrid" {
		if len(sels) == 0 {
			sels = []string{"s1", "s2"}
		}
		if include == "" {
			include = "sendgrid.net"
		}
	}
	return domain, sels, include
}
