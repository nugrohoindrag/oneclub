package sales

// Automatic lead capture (FR-LEAD-02, EP-20 FR-WEB-P3-02, EP-24
// FR-INT-P3-01/02): the website inquiry form (public, rate-limited,
// honeypot like P2's public forms), e-mail inquiries posted by the mail
// forwarder of the department mailboxes (API key), WhatsApp messages to the
// club's business number (integration.webhook_received) and the website
// contact form about a sales topic. A message from a contact with an open
// lead is added to that lead instead of creating a duplicate.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/outbox"
)

// lineKeywords infer the business line of a free-text inquiry.
var lineKeywords = []struct {
	line  string
	words []string
}{
	{"wedding", []string{"wedding", "pernikahan", "nikah", "resepsi", "akad", "pengantin"}},
	{"tournament", []string{"tournament", "turnamen"}},
	{"mice", []string{"meeting", "mice", "conference", "seminar", "rapat", "konferensi", "workshop", "training"}},
	{"membership", []string{"membership", "keanggotaan"}},
	{"stay", []string{"bungalow", "villa", "menginap", "vip suite"}},
	{"event", []string{"birthday", "ulang tahun", "gathering", "party", "pesta", "arisan"}},
	{"banquet", []string{"banquet", "ballroom", "catering"}},
	{"golf", []string{"golf"}},
	{"package", []string{"package", "paket"}},
}

// inferLine guesses the business line of an inquiry text.
func inferLine(text string) string {
	t := strings.ToLower(text)
	for _, k := range lineKeywords {
		for _, w := range k.words {
			if strings.Contains(t, w) {
				return k.line
			}
		}
	}
	return "other"
}

// appendInbound records an inbound message on an existing open lead.
func (m *Module) appendInbound(ctx context.Context, tx pgx.Tx, property, lid uuid.UUID, typ, source, subject, text, ref string, at time.Time) error {
	_, err := m.addActivity(ctx, tx, property, activityTarget{lead: &lid}, ActivityInput{Type: typ, Direction: "inbound", Subject: subject,
		Notes: text, OccurredAt: &at}, source, ref)
	return err
}

// ── website inquiry (public) ──────────────────────────────────────────────

// InquiryInput is the website inquiry form (Wedding & Banquet, MICE,
// corporate golf, tournament, membership pages).
type InquiryInput struct {
	PropertyID  uuid.UUID       `json:"propertyId"`
	Guest       crm.PublicGuest `json:"guest"`
	CompanyName string          `json:"companyName,omitempty"`
	Line        string          `json:"line,omitempty" enum:"wedding,banquet,mice,event,tournament,stay,golf,package,membership,other" doc:"Default: inferred from the message"`
	EventType   string          `json:"eventType,omitempty" enum:"wedding,meeting,conference,gathering,birthday,tournament,other"`
	EventDate   *route.Date     `json:"eventDate,omitempty"`
	Pax         *int            `json:"pax,omitempty"`
	Budget      string          `json:"budget,omitempty"`
	Message     string          `json:"message"`
	Consent     bool            `json:"consent,omitempty" doc:"Marketing consent (UU PDP)"`
	Channel     string          `json:"channel,omitempty" doc:"Page or campaign (UTM) of the form"`
}

// InquiryResult acknowledges an inquiry.
type InquiryResult struct {
	Status    string `json:"status" enum:"received"`
	Reference string `json:"reference,omitempty" doc:"Lead number"`
}

// publicInquiry handles POST /api/v1/public/inquiries.
func (m *Module) publicInquiry(w http.ResponseWriter, r *http.Request) {
	if !crm.PublicLimiter.Allow(httpx.ClientIP(r) + r.URL.Path) {
		httpx.WriteError(w, r, errs.RateLimited())
		return
	}
	var in InquiryInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if in.PropertyID == uuid.Nil {
		httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
		return
	}
	ctx := crm.PublicCtx(r.Context(), in.PropertyID)
	if in.Guest.Website != "" {
		// Bots fill every field: accept silently and keep an audit trace.
		_ = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			pid := in.PropertyID
			return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "bot_dropped", EntityType: "public.request", EntityID: r.URL.Path,
				EntityLabel: httpx.ClientIP(r), PropertyID: &pid})
		})
		httpx.JSON(w, http.StatusAccepted, InquiryResult{Status: "received"})
		return
	}
	var out InquiryResult
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.properties WHERE id = $1 AND status = 'active')`, in.PropertyID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errs.BadRequest("invalid_property", "unknown property")
		}
		if err := handle.Required("message", in.Message); err != nil {
			return err
		}
		line := in.Line
		if line == "" {
			line = inferLine(in.Message + " " + in.EventType)
		}
		spec := leadSpec{LeadInput: LeadInput{Name: in.Guest.Name, CompanyName: in.CompanyName, Phone: in.Guest.Phone, Email: in.Guest.Email,
			Source: "website_form", Channel: in.Channel, Line: line, EventType: in.EventType, EventDate: in.EventDate, Pax: in.Pax, Budget: in.Budget,
			Message: in.Message, MarketingConsent: in.Consent}, consentSource: "website", activityType: "note", activitySource: "website"}
		if err := spec.validate(); err != nil {
			return err
		}
		now := clock.Now()
		if lid, err := openLeadByContact(ctx, tx, in.PropertyID, spec.Phone, spec.Email); err != nil {
			return err
		} else if lid != nil {
			if err := m.appendInbound(ctx, tx, in.PropertyID, *lid, "note", "website", "Website inquiry", in.Message, "", now); err != nil {
				return err
			}
			if in.Consent {
				if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET marketing_consent = true, consent_at = coalesce(consent_at, now()),
					consent_source = coalesce(consent_source, 'website') WHERE id = $1`, *lid); err != nil {
					return err
				}
			}
			l, err := GetLead(ctx, tx, *lid)
			out = InquiryResult{Status: "received", Reference: l.Number}
			return err
		}
		l, err := m.createLead(ctx, tx, in.PropertyID, spec)
		out = InquiryResult{Status: "received", Reference: l.Number}
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, out)
}

// ── e-mail capture ────────────────────────────────────────────────────────

// EmailLeadInput is an e-mail received by a department mailbox (posted by
// the mail forwarder with an API key holding crm.lead.capture).
type EmailLeadInput struct {
	From       string     `json:"from" doc:"Sender address"`
	FromName   string     `json:"fromName,omitempty"`
	To         string     `json:"to" doc:"Department mailbox, e.g. banquet@club.id"`
	Subject    string     `json:"subject,omitempty"`
	Text       string     `json:"text,omitempty"`
	MessageID  string     `json:"messageId" doc:"Message-ID (captured once)"`
	ReceivedAt *time.Time `json:"receivedAt,omitempty"`
}

// CaptureResult reports what an automatic capture did.
type CaptureResult struct {
	Status string    `json:"status" enum:"created,appended,duplicate"`
	LeadID uuid.UUID `json:"leadId"`
	Number string    `json:"number"`
}

// CaptureEmail turns an inbound e-mail into a lead (FR-INT-P3-02).
func (m *Module) CaptureEmail(ctx context.Context, tx pgx.Tx, property uuid.UUID, in EmailLeadInput) (CaptureResult, error) {
	in.From = strings.ToLower(strings.TrimSpace(in.From))
	if !emailRe.MatchString(in.From) {
		return CaptureResult{}, handle.Invalid("from", "invalid_email", "the sender address is required")
	}
	if err := handle.Required("messageId", in.MessageID); err != nil {
		return CaptureResult{}, err
	}
	at := clock.Now()
	if in.ReceivedAt != nil {
		at = *in.ReceivedAt
	}
	dupe := func(lid uuid.UUID) (CaptureResult, error) {
		l, err := GetLead(ctx, tx, lid)
		if err != nil {
			return CaptureResult{}, err
		}
		return CaptureResult{Status: "duplicate", LeadID: lid, Number: l.Number}, audit.Record(ctx, tx, audit.Entry{Module: "crm",
			Action: "capture_duplicate", EntityType: "crm.lead", EntityID: lid.String(), EntityLabel: l.Number, PropertyID: &property,
			Metadata: map[string]any{"messageId": in.MessageID}})
	}
	var lid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM crm.sales_leads WHERE property_id = $1 AND source = 'email' AND source_ref = $2
		UNION ALL SELECT lead_id FROM crm.sales_activities WHERE property_id = $1 AND source = 'email' AND external_ref = $2 AND lead_id IS NOT NULL
		LIMIT 1`, property, in.MessageID).Scan(&lid)
	if err == nil {
		return dupe(lid)
	}
	if !dbtx.IsNoRows(err) {
		return CaptureResult{}, err
	}
	subject := strings.TrimSpace(in.Subject)
	if subject == "" {
		subject = "E-mail inquiry"
	}
	text := strings.TrimSpace(subject + "\n\n" + in.Text)
	if existing, err := openLeadByContact(ctx, tx, property, "", in.From); err != nil {
		return CaptureResult{}, err
	} else if existing != nil {
		if err := m.appendInbound(ctx, tx, property, *existing, "email", "email", subject, in.Text, in.MessageID, at); err != nil {
			return CaptureResult{}, err
		}
		l, err := GetLead(ctx, tx, *existing)
		return CaptureResult{Status: "appended", LeadID: *existing, Number: l.Number}, err
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return CaptureResult{}, err
	}
	line := ""
	if box, _, ok := strings.Cut(strings.ToLower(strings.TrimSpace(in.To)), "@"); ok {
		line = pol.EmailLines[box]
	}
	if line == "" || line == "other" {
		if l := inferLine(text); l != "other" || line == "" {
			line = l
		}
	}
	name := strings.TrimSpace(in.FromName)
	if name == "" {
		name, _, _ = strings.Cut(in.From, "@")
	}
	l, err := m.createLead(ctx, tx, property, leadSpec{LeadInput: LeadInput{Name: name, Email: in.From, Source: "email", Channel: strings.TrimSpace(in.To),
		Line: line, Message: text}, sourceRef: in.MessageID, activityType: "email", activitySource: "email", activityRef: in.MessageID, createdAt: &at})
	if err != nil {
		return CaptureResult{}, err
	}
	return CaptureResult{Status: "created", LeadID: l.ID, Number: l.Number}, nil
}

// ── WhatsApp inbound (integration.webhook_received) ───────────────────────

// waMessage is an inbound WhatsApp message of a messaging webhook (the
// adapter forwards data.messages; the Cloud API raw shape is accepted too).
type waMessage struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Name      string `json:"name"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
}

type waWebhook struct {
	IntegrationCode string `json:"integrationCode"`
	Capability      string `json:"capability"`
	Data            struct {
		Messages []waMessage `json:"messages"`
		Entry    []struct {
			Changes []struct {
				Value struct {
					Contacts []struct {
						WaID    string `json:"wa_id"`
						Profile struct {
							Name string `json:"name"`
						} `json:"profile"`
					} `json:"contacts"`
					Messages []struct {
						ID        string `json:"id"`
						From      string `json:"from"`
						Timestamp string `json:"timestamp"`
						Text      struct {
							Body string `json:"body"`
						} `json:"text"`
					} `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	} `json:"data"`
}

// OnWebhook turns inbound WhatsApp messages from unknown numbers into
// leads; messages of an open lead are added to it and messages of known
// customers are logged in their Interaction History (FR-INT-P3-01).
func (m *Module) OnWebhook(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p waWebhook
	if err := e.Decode(&p); err != nil {
		return err
	}
	if p.Capability != "messaging" {
		return nil
	}
	msgs := p.Data.Messages
	for _, en := range p.Data.Entry {
		for _, ch := range en.Changes {
			names := map[string]string{}
			for _, c := range ch.Value.Contacts {
				names[c.WaID] = c.Profile.Name
			}
			for _, mm := range ch.Value.Messages {
				msgs = append(msgs, waMessage{ID: mm.ID, From: mm.From, Name: names[mm.From], Text: mm.Text.Body, Timestamp: mm.Timestamp})
			}
		}
	}
	for _, msg := range msgs {
		if strings.TrimSpace(msg.From) == "" || strings.TrimSpace(msg.Text) == "" {
			continue
		}
		if err := m.whatsAppInbound(ctx, tx, p.IntegrationCode, msg, e.OccurredAt); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) whatsAppInbound(ctx context.Context, tx pgx.Tx, integration string, msg waMessage, at time.Time) error {
	phone := crm.NormalizePhone(msg.From)
	if phone == "" {
		return nil
	}
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	if msg.ID != "" {
		var seen bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.sales_activities WHERE source = 'whatsapp' AND external_ref = $1)
			OR EXISTS (SELECT 1 FROM crm.sales_leads WHERE source = 'whatsapp' AND source_ref = $1)`, msg.ID).Scan(&seen); err != nil {
			return err
		}
		if seen {
			return nil
		}
	}
	// an open lead of the number (any property)
	var lid, property uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id, property_id FROM crm.sales_leads WHERE phone = $1 AND status IN ('new', 'contacted', 'qualified')
		ORDER BY created_at DESC LIMIT 1`, phone).Scan(&lid, &property)
	if err == nil {
		return m.appendInbound(ctx, tx, property, lid, "whatsapp", "whatsapp", "WhatsApp message", msg.Text, msg.ID, at)
	}
	if !dbtx.IsNoRows(err) {
		return err
	}
	// a known customer: Interaction History only
	var cid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id, property_id FROM crm.customers WHERE phone = $1 AND status = 'active' AND archived_at IS NULL
		ORDER BY created_at LIMIT 1`, phone).Scan(&cid, &property)
	if err == nil {
		_, err := crm.LogInteraction(ctx, tx, property, cid, crm.InteractionInput{Channel: "whatsapp", Direction: "inbound", Subject: "WhatsApp message",
			Body: msg.Text, OccurredAt: &at}, "manual", "whatsapp.inbound", nil)
		return err
	}
	if !dbtx.IsNoRows(err) {
		return err
	}
	property, err = m.whatsAppProperty(ctx, tx, integration)
	if err != nil || property == uuid.Nil {
		return err
	}
	name := strings.TrimSpace(msg.Name)
	if name == "" {
		name = "WhatsApp " + phone
	}
	_, err = m.createLead(ctx, tx, property, leadSpec{LeadInput: LeadInput{Name: name, Phone: phone, Source: "whatsapp", Channel: integration,
		Line: inferLine(msg.Text), Message: msg.Text}, sourceRef: msg.ID, activityType: "whatsapp", activitySource: "whatsapp", createdAt: &at})
	return err
}

// whatsAppProperty is the property whose Sales Policies list the messaging
// integration, else the first property.
func (m *Module) whatsAppProperty(ctx context.Context, tx pgx.Tx, integration string) (uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active' ORDER BY created_at`)
	if err != nil {
		return uuid.Nil, err
	}
	props, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(props) == 0 {
		return uuid.Nil, err
	}
	for _, p := range props {
		pol, _, err := LoadPolicy(ctx, tx, p)
		if err != nil {
			return uuid.Nil, err
		}
		if oneOf(pol.WhatsAppIntegrations, integration) {
			return p, nil
		}
	}
	return props[0], nil
}

// ── website contact form ──────────────────────────────────────────────────

// contactTopics maps the topics of the P2 contact form to business lines.
var contactTopics = map[string]string{"wedding": "wedding", "banquet": "banquet", "meeting": "mice", "mice": "mice", "event": "event",
	"tournament": "tournament", "membership": "membership", "golf": "golf", "bungalow": "stay", "stay": "stay", "package": "package"}

// ContactHook makes a website contact message about a sales topic a lead
// (registered with crm.SetSalesContactHook by internal/app).
func (m *Module) ContactHook(ctx context.Context, tx pgx.Tx, property uuid.UUID, customer crm.Customer, guest crm.PublicGuest, topic, message string) error {
	line, ok := contactTopics[strings.ToLower(strings.TrimSpace(topic))]
	if !ok {
		return nil
	}
	now := clock.Now()
	if lid, err := openLeadByContact(ctx, tx, property, guest.Phone, guest.Email); err != nil {
		return err
	} else if lid != nil {
		return m.appendInbound(ctx, tx, property, *lid, "note", "website", "Website contact · "+topic, message, "", now)
	}
	cid := customer.ID
	spec := leadSpec{LeadInput: LeadInput{Name: guest.Name, Phone: guest.Phone, Email: guest.Email, Source: "website_form", Channel: "contact form",
		Line: line, Message: message, CustomerID: &cid}, consentSource: "website", activityType: "note", activitySource: "website"}
	if err := spec.validate(); err != nil {
		return nil //nolint:nilerr // the contact itself is accepted; an incomplete contact does not become a lead
	}
	_, err := m.createLead(ctx, tx, property, spec)
	return err
}
