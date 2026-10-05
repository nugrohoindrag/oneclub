// Package notification is the single notification service (EP-05): template
// per event + channel + language, in-app notification center, asynchronous
// e-mail / WhatsApp delivery through background jobs with retry and backoff,
// delivery history and per-user opt-out.
package notification

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"slices"
	"strings"
	"text/template"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notify"
)

// Category is a preference category; mandatory ones cannot be opted out.
type Category struct {
	Code      string `json:"code"`
	Label     string `json:"label"`
	Mandatory bool   `json:"mandatory"`
}

// Categories (FR-NOT-06).
var Categories = []Category{
	{Code: "security", Label: "Security", Mandatory: true},
	{Code: "approval", Label: "Approvals", Mandatory: false},
	{Code: "report", Label: "Reports", Mandatory: false},
	{Code: "system", Label: "System", Mandatory: false},
	{Code: "general", Label: "General", Mandatory: false},
}

func categoryMandatory(code string) bool {
	for _, c := range Categories {
		if c.Code == code {
			return c.Mandatory
		}
	}
	return false
}

// Service implements notify.Sender.
type Service struct {
	DB           *dbtx.DB
	Jobs         *jobs.Client
	Integrations *integration.Service
	Cfg          *config.Config
}

var _ notify.Sender = (*Service)(nil)

type recipient struct {
	ID     uuid.UUID
	Email  string
	Name   string
	Phone  *string
	Locale string
}

// Send renders and queues m for every recipient and channel inside tx.
func (s *Service) Send(ctx context.Context, tx pgx.Tx, m notify.Message) error {
	channels := m.Channels
	if len(channels) == 0 {
		channels = []string{notify.ChannelInApp, notify.ChannelEmail}
	}
	if m.Category == "" {
		m.Category = "general"
	}
	mandatory := m.Mandatory || categoryMandatory(m.Category)
	var defLocale string
	if err := tx.QueryRow(ctx, `SELECT default_locale FROM platform.instance`).Scan(&defLocale); err != nil {
		defLocale = "id"
	}
	var recips []recipient
	if len(m.UserIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT id, email, full_name, phone, coalesce(locale, $2) FROM platform.users
			WHERE id = ANY($1) AND status = 'active'`, m.UserIDs, defLocale)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r recipient
			if err := rows.Scan(&r.ID, &r.Email, &r.Name, &r.Phone, &r.Locale); err != nil {
				rows.Close()
				return err
			}
			recips = append(recips, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if m.Email != "" || m.Phone != "" {
		loc := m.Locale
		if loc == "" {
			loc = defLocale
		}
		r := recipient{Email: m.Email, Name: m.Name, Locale: loc}
		if m.Phone != "" {
			ph := m.Phone
			r.Phone = &ph
		}
		recips = append(recips, r)
	}
	for _, r := range recips {
		optedOut := map[string]bool{}
		if !mandatory && r.ID != uuid.Nil {
			rows, err := tx.Query(ctx, `SELECT channel FROM platform.notification_preferences WHERE user_id = $1 AND category = $2 AND NOT enabled`, r.ID, m.Category)
			if err != nil {
				return err
			}
			for rows.Next() {
				var ch string
				_ = rows.Scan(&ch)
				optedOut[ch] = true
			}
			rows.Close()
		}
		data := map[string]any{"recipientName": r.Name}
		for k, v := range m.Data {
			data[k] = v
		}
		if m.Link != "" {
			data["link"] = m.Link
		}
		for _, ch := range channels {
			if optedOut[ch] {
				continue
			}
			if ch == notify.ChannelInApp && r.ID == uuid.Nil {
				continue
			}
			if ch == notify.ChannelEmail && r.Email == "" {
				continue
			}
			subject, body, err := s.render(ctx, tx, m.Event, ch, r.Locale, data)
			if err != nil {
				return err
			}
			if err := s.queue(ctx, tx, m, r, ch, subject, body, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) queue(ctx context.Context, tx pgx.Tx, m notify.Message, r recipient, ch, subject, body string, data map[string]any) error {
	did := id.New()
	var uid *uuid.UUID
	if r.ID != uuid.Nil {
		uid = &r.ID
	}
	payload := map[string]any{}
	for k, v := range data {
		payload[k] = fmt.Sprint(v)
	}
	// Deliveries that are final at once keep no secret (one-time codes).
	_, finalSubject, finalBody := eraseSecrets(payload, subject, body)
	switch ch {
	case notify.ChannelInApp:
		if _, err := tx.Exec(ctx, `INSERT INTO platform.notifications (id, user_id, event_code, category, title, body, link)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, id.New(), r.ID, m.Event, m.Category, subject, body, nullable(m.Link)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.notification_deliveries (id, user_id, event_code, category, channel, locale, recipient,
			subject, body, status, attempts, sent_at) VALUES ($1,$2,$3,$4,'in_app',$5,$6,$7,$8,'sent',1,now())`,
			did, uid, m.Event, m.Category, r.Locale, r.Name, finalSubject, finalBody)
		return err
	case notify.ChannelEmail, notify.ChannelWhatsApp:
		to := r.Email
		if ch == notify.ChannelWhatsApp {
			if r.Phone == nil || *r.Phone == "" {
				_, err := tx.Exec(ctx, `INSERT INTO platform.notification_deliveries (id, user_id, event_code, category, channel, locale,
					recipient, subject, body, status, last_error) VALUES ($1,$2,$3,$4,'whatsapp',$5,'-',$6,$7,'skipped','recipient has no phone number')`,
					did, uid, m.Event, m.Category, r.Locale, finalSubject, finalBody)
				return err
			}
			to = *r.Phone
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.notification_deliveries (id, user_id, event_code, category, channel, locale,
			recipient, subject, body, status, payload) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10)`,
			did, uid, m.Event, m.Category, ch, r.Locale, to, subject, body, payload); err != nil {
			return err
		}
		jid, err := s.Jobs.Insert(ctx, tx, DeliverArgs{DeliveryID: did}, nil)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE platform.notification_deliveries SET job_id = $2 WHERE id = $1`, did, jid)
		return err
	default:
		return fmt.Errorf("notification: unknown channel %q", ch)
	}
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// render picks the template for (event, channel, locale) with fallbacks:
// channel → email template; locale → other locale.
func (s *Service) render(ctx context.Context, q dbtx.Querier, event, channel, locale string, data map[string]any) (string, string, error) {
	var subj, body string
	err := q.QueryRow(ctx, `
		SELECT subject, body FROM platform.notification_templates
		WHERE event_code = $1 AND is_active
		ORDER BY (channel = $2) DESC, (channel = 'email') DESC, (locale = $3) DESC, (locale = 'en') DESC
		LIMIT 1`, event, channel, locale).Scan(&subj, &body)
	if dbtx.IsNoRows(err) {
		subj, body = event, "{{range $k, $v := .}}{{$k}}: {{$v}}\n{{end}}"
	} else if err != nil {
		return "", "", err
	}
	rs, err := execTemplate(subj, data)
	if err != nil {
		return "", "", err
	}
	rb, err := execTemplate(body, data)
	return rs, rb, err
}

func execTemplate(src string, data map[string]any) (string, error) {
	t, err := template.New("t").Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", fmt.Errorf("notification template: %w", err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.ReplaceAll(b.String(), "<no value>", ""), nil
}

// ValidateTemplate checks a template parses.
func ValidateTemplate(src string) error {
	_, err := template.New("t").Option("missingkey=zero").Parse(src)
	return err
}

// Branding is what e-mail layouts use (FR-BRD-01).
type Branding struct {
	AppName         string
	EmailSenderName string
	PrimaryColor    string
	LogoURL         string
}

func (s *Service) branding(ctx context.Context) Branding {
	b := Branding{AppName: "OneClub", PrimaryColor: "#6FAF39"}
	var name string
	var raw map[string]any
	if err := s.DB.Primary.QueryRow(ctx, `SELECT name, branding FROM platform.instance`).Scan(&name, &raw); err == nil {
		b.AppName = name
		if v, _ := raw["appName"].(string); v != "" {
			b.AppName = v
		}
		if v, _ := raw["emailSenderName"].(string); v != "" {
			b.EmailSenderName = v
		}
		if v, _ := raw["primaryColor"].(string); v != "" {
			b.PrimaryColor = v
		}
		if v, _ := raw["logoUrl"].(string); v != "" {
			if strings.HasPrefix(v, "/") {
				v = s.Cfg.PublicBaseURL + v
			}
			b.LogoURL = v
		}
	}
	if b.EmailSenderName == "" {
		b.EmailSenderName = b.AppName
	}
	return b
}

// EmailHTML wraps a plain-text body in the branded layout.
func EmailHTML(b Branding, subject, text string) string {
	paras := []string{}
	for _, p := range strings.Split(strings.TrimSpace(text), "\n\n") {
		paras = append(paras, "<p style=\"margin:0 0 16px\">"+strings.ReplaceAll(linkify(html.EscapeString(p)), "\n", "<br>")+"</p>")
	}
	logo := "<div style=\"font-size:20px;font-weight:700;color:#111\">" + html.EscapeString(b.AppName) + "</div>"
	if b.LogoURL != "" {
		logo = "<img src=\"" + html.EscapeString(b.LogoURL) + "\" alt=\"" + html.EscapeString(b.AppName) + "\" height=\"40\" style=\"display:block\">"
	}
	return `<!doctype html><html><body style="margin:0;background:#f3f5f1;font-family:Inter,Roboto,Arial,sans-serif;color:#1b1d18">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td align="center" style="padding:32px 16px">
<table role="presentation" width="560" cellpadding="0" cellspacing="0" style="max-width:560px;background:#ffffff;border-radius:24px;overflow:hidden">
<tr><td style="padding:28px 32px 8px">` + logo + `</td></tr>
<tr><td style="padding:0 32px"><div style="height:4px;width:48px;border-radius:2px;background:` + html.EscapeString(b.PrimaryColor) + `"></div></td></tr>
<tr><td style="padding:24px 32px 8px"><h1 style="margin:0 0 16px;font-size:20px;line-height:28px">` + html.EscapeString(subject) + `</h1>` +
		strings.Join(paras, "") + `</td></tr>
<tr><td style="padding:16px 32px 28px;font-size:12px;color:#6b7065">` + html.EscapeString(b.AppName) + ` · OneClub</td></tr>
</table></td></tr></table></body></html>`
}

func linkify(s string) string {
	words := strings.Fields(s)
	for _, w := range words {
		if strings.HasPrefix(w, "http://") || strings.HasPrefix(w, "https://") {
			s = strings.Replace(s, w, `<a href="`+w+`" style="color:#1b1d18">`+w+`</a>`, 1)
		}
	}
	return s
}

// UserIDsWithPermission returns active users who hold perm (optionally at a
// property); used to address approvers and Platform Admin alerts.
func UserIDsWithPermission(ctx context.Context, q dbtx.Querier, perm string, propertyID *uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT u.id FROM platform.users u
		JOIN platform.role_assignments ra ON ra.user_id = u.id
		JOIN platform.roles r ON r.id = ra.role_id AND r.status = 'active'
		JOIN platform.role_permissions rp ON rp.role_id = r.id AND rp.permission_code = $1
		WHERE u.status = 'active' AND ($2::uuid IS NULL OR ra.property_id IS NULL OR ra.property_id = $2)`, perm, propertyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		if !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}
