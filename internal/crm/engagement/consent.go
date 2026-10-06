package engagement

// Communication Preferences & consent (FR-CMP-02, FR-APP-P3-08, UU PDP):
// opt-in per channel, the consent history, the suppression list and the
// public unsubscribe link. Without an explicit preference, e-mail and in-app
// follow P1's marketing opt-in; WhatsApp needs an explicit opt-in.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// Channels of marketing communication.
var Channels = []string{"email", "whatsapp", "in_app"}

// Suppressions is the suppression list (bounces, complaints, legal requests).
var Suppressions = &resource.Def{
	Key: "crm.suppression", Module: "crm", Perm: "crm.suppression", Path: "/api/v1/crm/suppressions", Table: "crm.suppressions",
	Name: "Suppression", Plural: "Suppression List", Tag: "Campaigns", PropertyScoped: true, OrderBy: "created_at DESC, id",
	Fields: []resource.Field{
		{Name: "channel", Column: "channel", Label: "Channel", Kind: resource.Enum, Enum: []string{"email", "whatsapp"}, Required: true, Filter: true},
		{Name: "address", Column: "address", Label: "E-mail / Phone", Kind: resource.String, Required: true, Max: 254, Search: true},
		{Name: "customerId", Column: "customer_id", Label: "Customer", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "crm.customers", SameProperty: true, Label: "customer"}},
		{Name: "reason", Column: "reason", Label: "Reason", Kind: resource.Text, Max: 500},
		resource.Status("active", "inactive")},
}

func init() {
	Suppressions.Hooks.BeforeWrite = func(_ context.Context, _ pgx.Tx, v, before map[string]any) error {
		ch, _ := v["channel"].(string)
		if ch == "" && before != nil {
			ch, _ = before["channel"].(string)
		}
		if a, ok := v["address"].(string); ok {
			v["address"] = normalizeAddress(ch, a)
			if v["address"] == "" {
				return handle.Invalid("address", "invalid_address", "an e-mail address or phone number is required")
			}
		}
		return nil
	}
}

func normalizeAddress(channel, a string) string {
	a = strings.TrimSpace(a)
	if channel == "whatsapp" {
		return crm.NormalizePhone(a)
	}
	return strings.ToLower(a)
}

// ChannelPreference is the consent of one channel.
type ChannelPreference struct {
	Channel    string     `json:"channel" enum:"email,whatsapp,in_app"`
	OptedIn    bool       `json:"optedIn" doc:"Effective consent"`
	Explicit   bool       `json:"explicit" doc:"false: derived from the marketing opt-in (e-mail, in-app)"`
	Source     *string    `json:"source"`
	UpdatedAt  *time.Time `json:"updatedAt"`
	Suppressed bool       `json:"suppressed" doc:"The address is on the suppression list"`
}

// CommunicationPreferences are the channel consents of a customer.
type CommunicationPreferences struct {
	CustomerID     uuid.UUID           `json:"customerId"`
	MarketingOptIn bool                `json:"marketingOptIn" doc:"P1 marketing consent (master switch)"`
	Channels       []ChannelPreference `json:"channels"`
}

// CommunicationPreferencesInput changes channel consents; omitted channels are unchanged.
type CommunicationPreferencesInput struct {
	Email    *bool `json:"email,omitempty"`
	WhatsApp *bool `json:"whatsapp,omitempty"`
	InApp    *bool `json:"inApp,omitempty"`
}

func (in CommunicationPreferencesInput) values() map[string]*bool {
	return map[string]*bool{"email": in.Email, "whatsapp": in.WhatsApp, "in_app": in.InApp}
}

// Preferences loads the communication preferences of a customer.
func Preferences(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (CommunicationPreferences, error) {
	var property uuid.UUID
	var optIn bool
	var email, phone *string
	if err := q.QueryRow(ctx, `SELECT property_id, marketing_opt_in, email, phone FROM crm.customers WHERE id = $1`, customer).
		Scan(&property, &optIn, &email, &phone); err != nil {
		if dbtx.IsNoRows(err) {
			return CommunicationPreferences{}, errs.NotFound("customer")
		}
		return CommunicationPreferences{}, err
	}
	out := CommunicationPreferences{CustomerID: customer, MarketingOptIn: optIn}
	type row struct {
		Channel   string    `db:"channel"`
		OptedIn   bool      `db:"opted_in"`
		Source    string    `db:"source"`
		UpdatedAt time.Time `db:"updated_at"`
	}
	rows, err := handle.List[row](q.Query(ctx, `SELECT channel, opted_in, source, updated_at FROM crm.communication_preferences WHERE customer_id = $1`, customer))
	if err != nil {
		return out, err
	}
	byCh := map[string]row{}
	for _, r := range rows {
		byCh[r.Channel] = r
	}
	for _, ch := range Channels {
		p := ChannelPreference{Channel: ch, OptedIn: ch != "whatsapp" && optIn}
		if r, ok := byCh[ch]; ok {
			src, at := r.Source, r.UpdatedAt
			p.OptedIn, p.Explicit, p.Source, p.UpdatedAt = r.OptedIn, true, &src, &at
		}
		addr := ""
		switch ch {
		case "email":
			addr = normalizeAddress(ch, deref(email))
		case "whatsapp":
			addr = normalizeAddress(ch, deref(phone))
		}
		if addr != "" {
			if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.suppressions WHERE property_id = $1 AND channel = $2 AND status = 'active'
				AND (address = $3 OR customer_id = $4))`, property, ch, addr, customer).Scan(&p.Suppressed); err != nil {
				return out, err
			}
		}
		out.Channels = append(out.Channels, p)
	}
	return out, nil
}

// SetPreferences records channel consents (with history) and keeps P1's
// marketing opt-in as the master switch: on when any channel is opted in,
// off when every channel is opted out.
func SetPreferences(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, in CommunicationPreferencesInput, source, refType string, refID *uuid.UUID) (CommunicationPreferences, error) {
	before, err := Preferences(ctx, tx, customer)
	if err != nil {
		return before, err
	}
	changed := false
	for ch, v := range in.values() {
		if v == nil {
			continue
		}
		changed = true
		if _, err := tx.Exec(ctx, `INSERT INTO crm.communication_preferences (customer_id, channel, property_id, opted_in, source, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (customer_id, channel) DO UPDATE SET opted_in = EXCLUDED.opted_in, source = EXCLUDED.source,
			updated_at = now(), updated_by = EXCLUDED.updated_by`, customer, ch, property, *v, source, actor(ctx)); err != nil {
			return before, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.consent_events (id, property_id, customer_id, channel, opted_in, source, ref_type, ref_id, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id.New(), property, customer, ch, *v, source, nullStr(refType), refID, actor(ctx)); err != nil {
			return before, err
		}
	}
	if !changed {
		return before, handle.Invalid("channels", "required", "choose at least one channel")
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.customers c SET marketing_opt_in = CASE
		  WHEN EXISTS (SELECT 1 FROM crm.communication_preferences p WHERE p.customer_id = c.id AND p.opted_in) THEN true
		  WHEN (SELECT count(*) FROM crm.communication_preferences p WHERE p.customer_id = c.id AND NOT p.opted_in) >= 3 THEN false
		  ELSE c.marketing_opt_in END,
		consent_updated_at = now(), consent_at = coalesce(consent_at, now()) WHERE c.id = $1`, customer); err != nil {
		return before, err
	}
	after, err := Preferences(ctx, tx, customer)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "communication_preferences", EntityType: "crm.customer",
		EntityID: customer.String(), EntityLabel: "communication preferences", PropertyID: &property, Before: before, After: after,
		Metadata: map[string]any{"source": source}})
}

// recipientInfo is a customer as an addressee of marketing.
type recipientInfo struct {
	ID      uuid.UUID  `db:"id"`
	Name    string     `db:"name"`
	Email   *string    `db:"email"`
	Phone   *string    `db:"phone"`
	UserID  *uuid.UUID `db:"user_id"`
	Locale  string     `db:"locale"`
	Consent bool       `db:"consent"`
}

func (r recipientInfo) address(channel string) string {
	switch channel {
	case "email":
		return normalizeAddress(channel, deref(r.Email))
	case "whatsapp":
		return normalizeAddress(channel, deref(r.Phone))
	case "in_app":
		if r.UserID != nil {
			return r.UserID.String()
		}
	}
	return ""
}

// consentSQL selects customers with the effective consent of channel $2.
const consentSQL = `SELECT c.id, c.name, c.email, c.phone, c.user_id, coalesce(c.locale, '') AS locale,
	coalesce(p.opted_in, CASE WHEN $2 = 'whatsapp' THEN false ELSE c.marketing_opt_in END) AS consent
	FROM crm.customers c LEFT JOIN crm.communication_preferences p ON p.customer_id = c.id AND p.channel = $2`

// suppressedSet loads the suppressed addresses and customers of a channel.
func suppressedSet(ctx context.Context, q dbtx.Querier, property uuid.UUID, channel string) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT address, customer_id FROM crm.suppressions WHERE property_id = $1 AND channel = $2 AND status = 'active'`, property, channel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var a string
		var c *uuid.UUID
		if err := rows.Scan(&a, &c); err != nil {
			return nil, err
		}
		out[a] = true
		if c != nil {
			out[c.String()] = true
		}
	}
	return out, rows.Err()
}

// classify decides whether a customer can receive a marketing message on a
// channel: consent, suppression, contact.
func classify(r recipientInfo, channel string, suppressed map[string]bool) string {
	addr := r.address(channel)
	switch {
	case !r.Consent:
		return "skipped_no_consent"
	case (addr != "" && suppressed[addr]) || suppressed[r.ID.String()]:
		return "skipped_suppressed"
	case addr == "":
		return "skipped_no_contact"
	}
	return "queued"
}

// ── public unsubscribe ────────────────────────────────────────────────────

// UnsubscribeInfo is what the unsubscribe page shows.
type UnsubscribeInfo struct {
	Channel  string `json:"channel"`
	Address  string `json:"address" doc:"Masked"`
	Campaign string `json:"campaign"`
	Status   string `json:"status" enum:"subscribed,unsubscribed"`
}

// UnsubscribeInput confirms an unsubscribe.
type UnsubscribeInput struct {
	AllChannels bool `json:"allChannels,omitempty" doc:"Stop every marketing channel, not only this one"`
}

type tokenInfo struct {
	RecipientID uuid.UUID
	PropertyID  uuid.UUID
	CustomerID  uuid.UUID
	Channel     string
	Address     string
	Campaign    string
	Target      *string
}

func recipientByToken(ctx context.Context, q dbtx.Querier, tok string) (tokenInfo, error) {
	var t tokenInfo
	var addr *string
	err := q.QueryRow(ctx, `SELECT r.id, r.property_id, r.customer_id, r.channel, r.address, c.name, c.target_url FROM crm.campaign_recipients r
		JOIN crm.campaigns c ON c.id = r.campaign_id WHERE r.token = $1`, tok).Scan(&t.RecipientID, &t.PropertyID, &t.CustomerID, &t.Channel, &addr, &t.Campaign, &t.Target)
	if dbtx.IsNoRows(err) {
		return t, errs.NotFound("link")
	}
	t.Address = deref(addr)
	return t, err
}

func maskAddress(channel, a string) string {
	if a == "" {
		return ""
	}
	if channel == "email" {
		if at := strings.Index(a, "@"); at > 1 {
			return a[:1] + strings.Repeat("•", at-1) + a[at:]
		}
	}
	return mask.Phone(a)
}

// Unsubscribe opts the recipient of a campaign link out (FR-CMP-02).
func Unsubscribe(ctx context.Context, tx pgx.Tx, tok string, in UnsubscribeInput) (UnsubscribeInfo, error) {
	t, err := recipientByToken(ctx, tx, tok)
	if err != nil {
		return UnsubscribeInfo{}, err
	}
	f := false
	pin := CommunicationPreferencesInput{}
	switch {
	case in.AllChannels:
		pin = CommunicationPreferencesInput{Email: &f, WhatsApp: &f, InApp: &f}
	case t.Channel == "email":
		pin.Email = &f
	case t.Channel == "whatsapp":
		pin.WhatsApp = &f
	default:
		pin.InApp = &f
	}
	if _, err := SetPreferences(ctx, tx, t.PropertyID, t.CustomerID, pin, "unsubscribe", "crm.campaign_recipient", &t.RecipientID); err != nil {
		return UnsubscribeInfo{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.campaign_recipients SET unsubscribed_at = coalesce(unsubscribed_at, now()) WHERE id = $1`, t.RecipientID); err != nil {
		return UnsubscribeInfo{}, err
	}
	return UnsubscribeInfo{Channel: t.Channel, Address: maskAddress(t.Channel, t.Address), Campaign: t.Campaign, Status: "unsubscribed"}, nil
}

// unsubscribeInfo shows the state of an unsubscribe link.
func unsubscribeInfo(ctx context.Context, q dbtx.Querier, tok string) (UnsubscribeInfo, error) {
	t, err := recipientByToken(ctx, q, tok)
	if err != nil {
		return UnsubscribeInfo{}, err
	}
	p, err := Preferences(ctx, q, t.CustomerID)
	if err != nil {
		return UnsubscribeInfo{}, err
	}
	status := "unsubscribed"
	for _, c := range p.Channels {
		if c.Channel == t.Channel && c.OptedIn {
			status = "subscribed"
		}
	}
	return UnsubscribeInfo{Channel: t.Channel, Address: maskAddress(t.Channel, t.Address), Campaign: t.Campaign, Status: status}, nil
}

// trackClick records a click of a campaign link and returns the target.
func trackClick(ctx context.Context, tx pgx.Tx, tok string) (string, error) {
	t, err := recipientByToken(ctx, tx, tok)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.campaign_recipients SET clicked_at = coalesce(clicked_at, now()), click_count = click_count + 1 WHERE id = $1`,
		t.RecipientID); err != nil {
		return "", err
	}
	return deref(t.Target), nil
}
