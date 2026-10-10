package crm

// CRM Foundation (PRD P2 EP-23, EP-24): structured preferences, behaviour
// profile, personalised staff context, Customer 360 across business lines,
// interaction history, rule-based segmentation, feedback surveys and the
// campaign foundation.
//
// CRM sits below the business lines (Technical Doc §4.2), so it cannot read
// golf, sport club, stay or POS data itself. Those modules contribute
// Customer 360 sections, behaviour facts and segmentation facts through the
// provider hooks wired in internal/app.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/provision"
)

// SectionFunc loads one Customer 360 section (golf activity, POS …).
type SectionFunc func(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error)

// Behavior is what a business line knows about a customer's habits.
type Behavior struct {
	Facts      map[string]any `json:"facts"`
	Highlights []string       `json:"highlights" doc:"Short staff-facing hints, e.g. \"usually orders Es Teh Tawar\""`
}

// BehaviorFunc derives behaviour facts of one business line (FR-PRF-02).
type BehaviorFunc func(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (Behavior, error)

// Facts are the per-customer inputs of segmentation rules (FR-CRM-03).
type Facts struct {
	MembershipTypes []string
	Programs        []string
	Visits          int
	Spend           decimal.Decimal
	// Sport Club court bookings in the window and the last court played
	// (docs/requirement-booking-sportclub-mgcc.md FR-87).
	CourtBookings int
	Sports        []string
	LastCourtPlay *time.Time
	// Bungalow stays in the window and the last stay (docs/requirement-booking-hotel-mgcc.md FR-H78).
	Stays    int
	LastStay *time.Time
}

// FactsFunc returns segmentation facts of every customer at a property for
// activity since a date.
type FactsFunc func(ctx context.Context, q dbtx.Querier, property uuid.UUID, since time.Time) (map[uuid.UUID]Facts, error)

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

// ── preferences (FR-PRF-01, FR-PRF-05) ────────────────────────────────────

// PreferenceCategories; diet and allergy are sensitive health data.
var PreferenceCategories = []string{"favorite_caddy", "golf_cart", "tee_time", "diet", "allergy", "food", "beverage", "facility", "note"}

var sensitiveCategories = []string{"diet", "allergy"}

type PreferenceInput struct {
	Category string     `json:"category" enum:"favorite_caddy,golf_cart,tee_time,diet,allergy,food,beverage,facility,note"`
	Value    string     `json:"value"`
	RefType  string     `json:"refType,omitempty" doc:"e.g. golf.caddy, commercial.product"`
	RefID    *uuid.UUID `json:"refId,omitempty"`
}

// Preference is a structured customer preference.
type Preference struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	CustomerID uuid.UUID  `json:"customerId" db:"customer_id"`
	Category   string     `json:"category" db:"category"`
	Value      string     `json:"value" db:"value"`
	RefType    *string    `json:"refType" db:"ref_type"`
	RefID      *uuid.UUID `json:"refId" db:"ref_id"`
	Sensitive  bool       `json:"sensitive" db:"sensitive"`
	Source     string     `json:"source" db:"source"`
	CreatedAt  time.Time  `json:"createdAt" db:"created_at"`
}

// RecordPreference stores a preference (staff, caddy or the member) in P1's
// customer_preferences (FR-CUS-06): the value is also the preference key,
// so the same category + value is not duplicated and a removed one comes
// back active.
func RecordPreference(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, in PreferenceInput, source string) (Preference, error) {
	if !slices.Contains(PreferenceCategories, in.Category) {
		return Preference{}, handle.Invalid("category", "invalid_category", "unknown preference category")
	}
	in.Value = strings.TrimSpace(in.Value)
	if err := handle.Required("value", in.Value); err != nil {
		return Preference{}, err
	}
	if _, err := GetCustomer(ctx, tx, customer); err != nil {
		return Preference{}, err
	}
	key := in.Value
	if r := []rune(key); len(r) > 60 {
		key = string(r[:60])
	}
	var existing uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM crm.customer_preferences WHERE customer_id = $1 AND category = $2 AND lower(pref_key) = lower($3)
		AND status = 'active'`, customer, in.Category, key).Scan(&existing)
	if err == nil {
		p, err := preference(ctx, tx, existing)
		if err != nil {
			return p, err
		}
		return p, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "record_duplicate", EntityType: "crm.preference", EntityID: existing.String(),
			EntityLabel: in.Category, PropertyID: &property, After: map[string]any{"customerId": customer, "source": source}})
	}
	if !dbtx.IsNoRows(err) {
		return Preference{}, err
	}
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO crm.customer_preferences (id, property_id, customer_id, category, pref_key, pref_value, ref_type, ref_id,
		sensitive, source, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		ON CONFLICT (customer_id, category, pref_key) DO UPDATE SET status = 'active', pref_value = EXCLUDED.pref_value, ref_type = EXCLUDED.ref_type,
		ref_id = EXCLUDED.ref_id, source = EXCLUDED.source, updated_by = EXCLUDED.updated_by
		RETURNING id`, id.New(), property, customer, in.Category, key, in.Value, nullStr(in.RefType), in.RefID,
		slices.Contains(sensitiveCategories, in.Category), source, actor(ctx)).Scan(&pid); err != nil {
		return Preference{}, err
	}
	p, err := preference(ctx, tx, pid)
	if err != nil {
		return p, err
	}
	return p, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.preference", EntityID: pid.String(),
		EntityLabel: in.Category, PropertyID: &property, After: map[string]any{"customerId": customer, "category": in.Category, "source": source,
			"sensitive": p.Sensitive}})
}

const preferenceSelect = `SELECT id, customer_id, category, coalesce(pref_value, pref_key) AS value, ref_type, ref_id, sensitive, source, created_at
	FROM crm.customer_preferences`

func preference(ctx context.Context, q dbtx.Querier, pid uuid.UUID) (Preference, error) {
	rows, err := q.Query(ctx, preferenceSelect+` WHERE id = $1`, pid)
	return handle.One[Preference](rows, err, "preference")
}

// ListPreferences lists active preferences; sensitive ones only when asked
// (callers check crm.preference.view_sensitive or the customer themself).
func ListPreferences(ctx context.Context, q dbtx.Querier, customer uuid.UUID, sensitive bool) ([]Preference, error) {
	return handle.List[Preference](q.Query(ctx, preferenceSelect+` WHERE customer_id = $1 AND status = 'active' AND ($2 OR NOT sensitive)
		ORDER BY category, created_at`, customer, sensitive))
}

// RemovePreference deactivates a preference.
func RemovePreference(ctx context.Context, tx pgx.Tx, property, pid uuid.UUID) error {
	p, err := preference(ctx, tx, pid)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.customer_preferences SET status = 'inactive', updated_by = $2 WHERE id = $1 AND status = 'active'`, pid, actor(ctx)); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionDelete, EntityType: "crm.preference", EntityID: pid.String(),
		EntityLabel: p.Category, PropertyID: &property, Before: map[string]any{"customerId": p.CustomerID, "category": p.Category}})
}

// SetConsent updates the profiling consent and P1's marketing opt-in (FR-PRF-05).
func SetConsent(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, profiling, marketing *bool) (CustomerProfile, error) {
	before, err := GetCustomerProfile(ctx, tx, customer)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.customers SET consent_profiling = coalesce($2, consent_profiling), marketing_opt_in = coalesce($3, marketing_opt_in),
		consent_updated_at = now(), consent_at = coalesce(consent_at, now()), updated_by = $4 WHERE id = $1`, customer, profiling, marketing, actor(ctx)); err != nil {
		return before, err
	}
	after, err := GetCustomerProfile(ctx, tx, customer)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionUpdate, EntityType: "crm.customer", EntityID: customer.String(),
		EntityLabel: after.Name + " · consent", PropertyID: &property,
		Before: map[string]any{"consentProfiling": before.ConsentProfiling, "marketingOptIn": before.MarketingOptIn},
		After:  map[string]any{"consentProfiling": after.ConsentProfiling, "marketingOptIn": after.MarketingOptIn}})
}

// ── behaviour profile & personalised context (FR-PRF-02/03) ───────────────

// BehaviorProfile is the derived profile of a customer.
type BehaviorProfile struct {
	CustomerID uuid.UUID           `json:"customerId"`
	Consent    bool                `json:"consent" doc:"false: profiling not consented — no behaviour is derived (UU PDP)"`
	Lines      map[string]Behavior `json:"lines"`
	ComputedAt time.Time           `json:"computedAt"`
}

// Profile derives the behaviour profile; nothing is derived without consent.
func (m *Engagement) Profile(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (BehaviorProfile, error) {
	p, err := GetCustomerProfile(ctx, q, customer)
	if err != nil {
		return BehaviorProfile{}, err
	}
	out := BehaviorProfile{CustomerID: customer, Consent: p.ConsentProfiling, Lines: map[string]Behavior{}, ComputedAt: clock.Now()}
	if !p.ConsentProfiling {
		return out, nil
	}
	keys := make([]string, 0, len(m.Behavior))
	for k := range m.Behavior {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b, err := m.Behavior[k](ctx, q, property, customer)
		if err != nil {
			return out, err
		}
		if b.Facts == nil {
			b.Facts = map[string]any{}
		}
		if b.Highlights == nil {
			b.Highlights = []string{}
		}
		out.Lines[k] = b
	}
	return out, nil
}

// CustomerContext is what staff see when a customer is identified (check-in,
// POS, caddy tablet) — FR-PRF-03. No personal data beyond what the task needs.
type CustomerContext struct {
	CustomerID  uuid.UUID    `json:"customerId"`
	Name        string       `json:"name"`
	Notes       *string      `json:"notes"`
	Preferences []Preference `json:"preferences"`
	Highlights  []string     `json:"highlights"`
}

// Context builds the personalised context for a staff touchpoint.
func (m *Engagement) Context(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID, sensitive bool) (CustomerContext, error) {
	p, err := GetCustomer(ctx, q, customer)
	if err != nil {
		return CustomerContext{}, err
	}
	var notes *string
	_ = q.QueryRow(ctx, `SELECT notes FROM crm.customers WHERE id = $1`, customer).Scan(&notes)
	prefs, err := ListPreferences(ctx, q, customer, sensitive)
	if err != nil {
		return CustomerContext{}, err
	}
	out := CustomerContext{CustomerID: customer, Name: p.Name, Notes: notes, Preferences: prefs, Highlights: []string{}}
	for _, pr := range prefs {
		switch pr.Category {
		case "favorite_caddy":
			out.Highlights = append(out.Highlights, "Favourite caddy: "+pr.Value)
		case "beverage", "food":
			out.Highlights = append(out.Highlights, "Likes "+pr.Value)
		case "allergy":
			out.Highlights = append(out.Highlights, "Allergy: "+pr.Value)
		}
	}
	prof, err := m.Profile(ctx, q, property, customer)
	if err != nil {
		return out, err
	}
	keys := make([]string, 0, len(prof.Lines))
	for k := range prof.Lines {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Highlights = append(out.Highlights, prof.Lines[k].Highlights...)
	}
	return out, nil
}

// ── Customer 360 (FR-CRM-01) ──────────────────────────────────────────────

// Customer360 extends P1's Customer 360 (Basic) overview — profile,
// handicap, memberships, accounts, relationships, preferences, statistics,
// recent history — with the sections every P2 business line contributes,
// the interaction history and feedback.
type Customer360 struct {
	Overview
	Sections     map[string]any    `json:"sections" doc:"golf, sportclub, stay, pos, vouchers, payments … contributed by each business line"`
	Interactions []Interaction     `json:"interactions"`
	Feedback     []Feedback        `json:"feedback"`
	Placeholders map[string]string `json:"placeholders" doc:"Banquet, Campaign and Loyalty arrive in P3 (\"live\" once their section is wired)"`
}

// hideSensitive drops health preferences (diet, allergy) unless the caller
// may see them (FR-PRF-05).
func hideSensitive(o *Overview, allowed bool) {
	if allowed {
		return
	}
	kept := o.Preferences[:0]
	for _, p := range o.Preferences {
		if !slices.Contains(sensitiveCategories, p.Category) {
			kept = append(kept, p)
		}
	}
	o.Preferences = kept
}

// View360 assembles the Customer 360.
func (m *Engagement) View360(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, sensitive bool) (Customer360, error) {
	cid, err := resolveMerged(ctx, tx, customer)
	if err != nil {
		return Customer360{}, err
	}
	o, err := buildOverview(ctx, tx, cid, property, 20)
	if err != nil {
		return Customer360{}, err
	}
	hideSensitive(&o, sensitive)
	out := Customer360{Overview: o, Sections: map[string]any{},
		Placeholders: map[string]string{"banquet": "Available in P3", "loyalty": "Available in P3", "campaignResponse": "Available in P3"}}
	for k, f := range m.Sections {
		v, err := f(ctx, tx, property, cid)
		if err != nil {
			return out, err
		}
		out.Sections[k] = v
		if _, ok := out.Placeholders[k]; ok {
			out.Placeholders[k] = "live" // filled by a PRD P3 section (FR-C360-01)
		}
	}
	if out.Interactions, err = m.Interactions(ctx, tx, cid, 20); err != nil {
		return out, err
	}
	out.Feedback, err = handle.List[Feedback](tx.Query(ctx, feedbackSelect+` WHERE f.customer_id = $1 ORDER BY f.created_at DESC LIMIT 20`, cid))
	return out, err

}

// ── interactions (FR-CRM-02) ──────────────────────────────────────────────

type InteractionInput struct {
	Channel    string     `json:"channel" enum:"phone,whatsapp,email,visit,in_app,other"`
	Direction  string     `json:"direction,omitempty" enum:"inbound,outbound"`
	Subject    string     `json:"subject"`
	Body       string     `json:"body,omitempty"`
	OccurredAt *time.Time `json:"occurredAt,omitempty"`
}

// Interaction is one contact with a customer (manual or automatic).
type Interaction struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Channel    string     `json:"channel" db:"channel"`
	Direction  string     `json:"direction" db:"direction"`
	Subject    string     `json:"subject" db:"subject"`
	Body       *string    `json:"body" db:"body"`
	Source     string     `json:"source" db:"source" enum:"manual,notification,campaign,feedback"`
	RefType    *string    `json:"refType" db:"ref_type"`
	RefID      *uuid.UUID `json:"refId" db:"ref_id"`
	OccurredAt time.Time  `json:"occurredAt" db:"occurred_at"`
}

// LogInteraction records an interaction.
func LogInteraction(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, in InteractionInput, source, refType string, refID *uuid.UUID) (Interaction, error) {
	if err := handle.Required("subject", in.Subject); err != nil {
		return Interaction{}, err
	}
	if in.Direction == "" {
		in.Direction = "outbound"
	}
	at := clock.Now()
	if in.OccurredAt != nil {
		at = *in.OccurredAt
	}
	iid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.interactions (id, property_id, customer_id, channel, direction, subject, body, source, ref_type, ref_id,
		occurred_at, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, iid, property, customer, in.Channel, in.Direction, in.Subject,
		nullStr(in.Body), source, nullStr(refType), refID, at, actor(ctx)); err != nil {
		return Interaction{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id, channel, direction, subject, body, source, ref_type, ref_id, occurred_at FROM crm.interactions WHERE id = $1`, iid)
	it, err := handle.One[Interaction](rows, err, "interaction")
	if err != nil {
		return it, err
	}
	return it, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.interaction", EntityID: iid.String(),
		EntityLabel: in.Subject, PropertyID: &property, After: it})
}

// Interactions merges logged interactions with notifications delivered to
// the customer (portal user or e-mail) — automatic history.
func (m *Engagement) Interactions(ctx context.Context, q dbtx.Querier, customer uuid.UUID, limit int) ([]Interaction, error) {
	return handle.List[Interaction](q.Query(ctx, `
		SELECT id, channel, direction, subject, body, source, ref_type, ref_id, occurred_at FROM crm.interactions WHERE customer_id = $1
		UNION ALL
		SELECT d.id, d.channel, 'outbound', coalesce(nullif(d.subject, ''), d.event_code), NULL, 'notification', 'platform.notification', NULL,
		  coalesce(d.sent_at, d.created_at)
		FROM platform.notification_deliveries d JOIN crm.customers c ON c.id = $1
		WHERE d.status IN ('sent', 'pending') AND ((c.user_id IS NOT NULL AND d.user_id = c.user_id) OR (c.email IS NOT NULL AND lower(d.recipient) = lower(c.email)))
		ORDER BY occurred_at DESC LIMIT $2`, customer, limit))
}

// ── segmentation (FR-CRM-03) ──────────────────────────────────────────────

// SegmentRules are the rule set of a segment; empty fields do not filter.
type SegmentRules struct {
	CustomerTypes   []string `json:"customerTypes,omitempty"`
	MembershipTypes []string `json:"membershipTypes,omitempty" doc:"Membership type codes"`
	Programs        []string `json:"programs,omitempty" doc:"golf | sportclub | …"`
	NonMembers      bool     `json:"nonMembers,omitempty"`
	MinVisits       *int     `json:"minVisits,omitempty"`
	MaxVisits       *int     `json:"maxVisits,omitempty"`
	MinSpend        string   `json:"minSpend,omitempty"`
	LookbackDays    int      `json:"lookbackDays,omitempty" doc:"Activity window; default 90"`
	MinAge          *int     `json:"minAge,omitempty"`
	MaxAge          *int     `json:"maxAge,omitempty"`
	Resident        *bool    `json:"resident,omitempty"`
	Gender          string   `json:"gender,omitempty"`
	// Sport Club (FR-87): e.g. futsal players ≥ 4×/month, or not played for 60 days.
	MinCourtBookings  *int     `json:"minCourtBookings,omitempty" doc:"Court bookings in the window at least"`
	Sports            []string `json:"sports,omitempty" doc:"Played one of these sports (facility codes, e.g. FUTSAL)"`
	CourtInactiveDays *int     `json:"courtInactiveDays,omitempty" doc:"Played a court before but not in the last N days"`
	// Bungalows (docs/requirement-booking-hotel-mgcc.md FR-H78): e.g. stayed twice this year, or not for 6 months.
	MinStays         *int `json:"minStays,omitempty" doc:"Bungalow stays in the window at least"`
	StayInactiveDays *int `json:"stayInactiveDays,omitempty" doc:"Stayed before but not in the last N days"`
}

type SegmentResult struct {
	SegmentID   uuid.UUID `json:"segmentId"`
	MemberCount int       `json:"memberCount"`
	ComputedAt  time.Time `json:"computedAt"`
}

// ComputeSegment evaluates the rules and stores the members.
func (m *Engagement) ComputeSegment(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (SegmentResult, error) {
	var raw []byte
	var code string
	if err := tx.QueryRow(ctx, `SELECT code, rules FROM crm.segments WHERE id = $1 AND property_id = $2 FOR UPDATE`, sid, property).Scan(&code, &raw); err != nil {
		if dbtx.IsNoRows(err) {
			return SegmentResult{}, errs.NotFound("segment")
		}
		return SegmentResult{}, err
	}
	var r SegmentRules
	if err := json.Unmarshal(raw, &r); err != nil {
		return SegmentResult{}, handle.Invalid("rules", "invalid_rules", "segment rules are not valid")
	}
	minSpend, err := handle.Decimal("minSpend", r.MinSpend, decimal.Zero)
	if err != nil {
		return SegmentResult{}, err
	}
	if r.LookbackDays <= 0 {
		r.LookbackDays = 90
	}
	now := clock.Now()
	facts := map[uuid.UUID]Facts{}
	if m.Facts != nil {
		if facts, err = m.Facts(ctx, tx, property, now.AddDate(0, 0, -r.LookbackDays)); err != nil {
			return SegmentResult{}, err
		}
	}
	list, err := profiles(ctx, tx, property)
	if err != nil {
		return SegmentResult{}, err
	}
	var members []uuid.UUID
	for _, p := range list {
		f := facts[p.ID]
		switch {
		case len(r.CustomerTypes) > 0 && !slices.Contains(r.CustomerTypes, p.CustomerType),
			len(r.MembershipTypes) > 0 && !overlaps(r.MembershipTypes, f.MembershipTypes),
			len(r.Programs) > 0 && !overlaps(r.Programs, f.Programs),
			r.NonMembers && len(f.Programs) > 0,
			r.MinVisits != nil && f.Visits < *r.MinVisits,
			r.MaxVisits != nil && f.Visits > *r.MaxVisits,
			minSpend.IsPositive() && f.Spend.LessThan(minSpend),
			r.Resident != nil && p.Resident != *r.Resident,
			r.Gender != "" && p.Gender != r.Gender,
			r.MinCourtBookings != nil && f.CourtBookings < *r.MinCourtBookings,
			len(r.Sports) > 0 && !overlaps(r.Sports, f.Sports),
			r.CourtInactiveDays != nil && (f.LastCourtPlay == nil || f.LastCourtPlay.After(now.AddDate(0, 0, -*r.CourtInactiveDays))),
			r.MinStays != nil && f.Stays < *r.MinStays,
			r.StayInactiveDays != nil && (f.LastStay == nil || f.LastStay.After(now.AddDate(0, 0, -*r.StayInactiveDays))):
			continue
		}
		if r.MinAge != nil || r.MaxAge != nil {
			age, ok := AgeOn(p.Customer, now)
			if !ok || (r.MinAge != nil && age < *r.MinAge) || (r.MaxAge != nil && age > *r.MaxAge) {
				continue
			}
		}
		members = append(members, p.ID)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM crm.segment_members WHERE segment_id = $1`, sid); err != nil {
		return SegmentResult{}, err
	}
	if len(members) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO crm.segment_members (segment_id, property_id, customer_id) SELECT $1, $2, unnest($3::uuid[])`,
			sid, property, members); err != nil {
			return SegmentResult{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.segments SET member_count = $2, computed_at = $3 WHERE id = $1`, sid, len(members), now); err != nil {
		return SegmentResult{}, err
	}
	res := SegmentResult{SegmentID: sid, MemberCount: len(members), ComputedAt: now}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "compute", EntityType: "crm.segment", EntityID: sid.String(),
		EntityLabel: code, PropertyID: &property, After: res})
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// SegmentMember is a customer in a segment.
type SegmentMember struct {
	CustomerID uuid.UUID `json:"customerId" db:"id"`
	Code       string    `json:"code" db:"code"`
	Name       string    `json:"name" db:"name"`
	Email      *string   `json:"email" db:"email"`
	Phone      *string   `json:"phone" db:"phone"`
}

// SegmentMembers lists the stored members of a segment.
func SegmentMembers(ctx context.Context, q dbtx.Querier, sid uuid.UUID) ([]SegmentMember, error) {
	return handle.List[SegmentMember](q.Query(ctx, `SELECT c.id, c.code, c.name, c.email, c.phone FROM crm.segment_members s
		JOIN crm.customers c ON c.id = s.customer_id WHERE s.segment_id = $1 ORDER BY c.name`, sid))
}

// ── feedback (FR-CRM-04) ──────────────────────────────────────────────────

// FeedbackRequest asks a customer for feedback after a visit.
type FeedbackRequest struct {
	PropertyID   uuid.UUID
	CustomerID   uuid.UUID
	ContextType  string // round | stay | class | fnb | sport | meeting | other
	ContextID    *uuid.UUID
	ContextLabel string
	SubjectType  string // e.g. golf.caddy_assignment — rated in the same survey
	SubjectID    *uuid.UUID
	Channel      string
}

// FeedbackInvite is a sent survey.
type FeedbackInvite struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Token       string    `json:"token" db:"token"`
	ContextType string    `json:"contextType" db:"context_type"`
	Label       *string   `json:"contextLabel" db:"context_label"`
	Status      string    `json:"status" db:"status"`
	ExpiresAt   time.Time `json:"expiresAt" db:"expires_at"`
}

// RequestFeedback creates the survey link and sends it (once per context).
func (m *Engagement) RequestFeedback(ctx context.Context, tx pgx.Tx, r FeedbackRequest) (*FeedbackInvite, error) {
	var existing uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM crm.feedback_requests WHERE context_type = $1 AND context_id IS NOT DISTINCT FROM $2 AND customer_id = $3`,
		r.ContextType, r.ContextID, r.CustomerID).Scan(&existing)
	if err == nil {
		inv, err := m.invite(ctx, tx, existing)
		return &inv, err
	}
	if !dbtx.IsNoRows(err) {
		return nil, err
	}
	p, err := GetCustomerProfile(ctx, tx, r.CustomerID)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	if r.Channel == "" {
		r.Channel = "email"
		if p.Email == "" && p.Phone != "" {
			r.Channel = "whatsapp"
		}
	}
	fid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.feedback_requests (id, property_id, customer_id, context_type, context_id, context_label, subject_type,
		subject_id, token, channel, expires_at, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, fid, r.PropertyID, r.CustomerID,
		r.ContextType, r.ContextID, nullStr(r.ContextLabel), nullStr(r.SubjectType), r.SubjectID, token, r.Channel, clock.Now().AddDate(0, 0, 14), actor(ctx)); err != nil {
		return nil, err
	}
	link := strings.TrimRight(m.PublicURL, "/") + "/feedback/" + token
	msg := notify.Message{Event: "crm.feedback_survey", Category: "feedback", PropertyID: &r.PropertyID, Link: link,
		Data: map[string]any{"name": p.Name, "context": r.ContextLabel, "link": link}}
	switch {
	case p.UserID != nil:
		msg.UserIDs = []uuid.UUID{*p.UserID}
		msg.Channels = []string{notify.ChannelInApp, notify.ChannelEmail}
		if r.Channel == "whatsapp" {
			msg.Channels = append(msg.Channels, notify.ChannelWhatsApp)
		}
	case p.Email != "":
		msg.Email, msg.Channels = p.Email, []string{notify.ChannelEmail}
		if p.Locale != "" {
			msg.Locale = p.Locale
		}
	}
	if m.Notify != nil && (len(msg.UserIDs) > 0 || msg.Email != "") {
		if err := m.Notify.Send(ctx, tx, msg); err != nil {
			return nil, err
		}
	}
	inv, err := m.invite(ctx, tx, fid)
	return &inv, err
}

func (m *Engagement) invite(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (FeedbackInvite, error) {
	rows, err := q.Query(ctx, `SELECT id, token, context_type, context_label, status, expires_at FROM crm.feedback_requests WHERE id = $1`, fid)
	return handle.One[FeedbackInvite](rows, err, "feedback request")
}

// FeedbackInput is a survey answer.
type FeedbackInput struct {
	Rating  int    `json:"rating" doc:"1–5"`
	NPS     *int   `json:"nps,omitempty" doc:"0–10, optional"`
	Comment string `json:"comment,omitempty"`
	// SubjectRating rates the subject of the survey (e.g. the caddy).
	SubjectRating  *int   `json:"subjectRating,omitempty"`
	SubjectComment string `json:"subjectComment,omitempty"`
}

// Feedback is a received answer.
type Feedback struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	CustomerID   *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName *string    `json:"customerName" db:"customer_name"`
	ContextType  string     `json:"contextType" db:"context_type"`
	ContextID    *uuid.UUID `json:"contextId" db:"context_id"`
	ContextLabel *string    `json:"contextLabel" db:"context_label"`
	Rating       int        `json:"rating" db:"rating"`
	NPS          *int       `json:"nps" db:"nps"`
	Comment      *string    `json:"comment" db:"comment"`
	Channel      string     `json:"channel" db:"channel"`
	LowScore     bool       `json:"lowScore" db:"low_score"`
	FollowUp     string     `json:"followUp" db:"follow_up" enum:"none,open,closed"`
	FollowUpNote *string    `json:"followUpNote" db:"follow_up_note"`
	CreatedAt    time.Time  `json:"createdAt" db:"created_at"`
}

const feedbackSelect = `SELECT f.id, f.customer_id, c.name AS customer_name, f.context_type, f.context_id, f.context_label, f.rating, f.nps, f.comment,
	f.channel, f.low_score, f.follow_up, f.follow_up_note, f.created_at FROM crm.feedback f LEFT JOIN crm.customers c ON c.id = f.customer_id`

// SubjectRatingHook receives the subject rating of a survey (e.g. golf rates
// the caddy of the assignment).
type SubjectRatingHook func(ctx context.Context, tx pgx.Tx, property uuid.UUID, customer *uuid.UUID, subjectID uuid.UUID, rating int, comment, channel string) error

// SubjectHooks by subject type.
var subjectHooks = map[string]SubjectRatingHook{}

// RegisterSubject plugs the handler of a survey subject type.
func RegisterSubject(subjectType string, h SubjectRatingHook) { subjectHooks[subjectType] = h }

// SubmitFeedback stores an answer for a token (public link) or directly.
func (m *Engagement) SubmitFeedback(ctx context.Context, tx pgx.Tx, token string, in FeedbackInput, channel string) (Feedback, error) {
	if in.Rating < 1 || in.Rating > 5 {
		return Feedback{}, handle.Invalid("rating", "invalid_rating", "rating must be between 1 and 5")
	}
	if in.NPS != nil && (*in.NPS < 0 || *in.NPS > 10) {
		return Feedback{}, handle.Invalid("nps", "invalid_nps", "NPS must be between 0 and 10")
	}
	var req struct {
		ID          uuid.UUID
		PropertyID  uuid.UUID
		CustomerID  *uuid.UUID
		ContextType string
		ContextID   *uuid.UUID
		Label       *string
		SubjectType *string
		SubjectID   *uuid.UUID
		Status      string
		ExpiresAt   time.Time
	}
	err := tx.QueryRow(ctx, `SELECT id, property_id, customer_id, context_type, context_id, context_label, subject_type, subject_id, status, expires_at
		FROM crm.feedback_requests WHERE token = $1 FOR UPDATE`, token).Scan(&req.ID, &req.PropertyID, &req.CustomerID, &req.ContextType, &req.ContextID,
		&req.Label, &req.SubjectType, &req.SubjectID, &req.Status, &req.ExpiresAt)
	if dbtx.IsNoRows(err) {
		return Feedback{}, errs.NotFound("feedback survey")
	}
	if err != nil {
		return Feedback{}, err
	}
	if req.Status == "answered" {
		return Feedback{}, errs.Conflict("already_answered", "this survey has already been answered")
	}
	if clock.Now().After(req.ExpiresAt) {
		return Feedback{}, errs.Conflict("expired", "this survey link has expired")
	}
	low := in.Rating <= 2 || (in.NPS != nil && *in.NPS <= 6)
	follow := "none"
	if low {
		follow = "open"
	}
	fid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.feedback (id, property_id, request_id, customer_id, context_type, context_id, context_label, rating, nps,
		comment, channel, low_score, follow_up) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, fid, req.PropertyID, req.ID, req.CustomerID,
		req.ContextType, req.ContextID, req.Label, in.Rating, in.NPS, nullStr(in.Comment), channel, low, follow); err != nil {
		return Feedback{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.feedback_requests SET status = 'answered' WHERE id = $1`, req.ID); err != nil {
		return Feedback{}, err
	}
	if in.SubjectRating != nil && req.SubjectType != nil && req.SubjectID != nil {
		if h, ok := subjectHooks[*req.SubjectType]; ok {
			if err := h(ctx, tx, req.PropertyID, req.CustomerID, *req.SubjectID, *in.SubjectRating, in.SubjectComment, channel); err != nil {
				return Feedback{}, err
			}
		}
	}
	if req.CustomerID != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO crm.interactions (id, property_id, customer_id, channel, direction, subject, body, source, ref_type, ref_id)
			VALUES ($1,$2,$3,'other','inbound',$4,$5,'feedback','crm.feedback',$6)`, id.New(), req.PropertyID, *req.CustomerID,
			"Feedback "+req.ContextType+" · "+strings.Repeat("★", in.Rating), nullStr(in.Comment), fid); err != nil {
			return Feedback{}, err
		}
	}
	if low && m.Notify != nil {
		users, err := notify.Holders(ctx, tx, req.PropertyID, "crm.feedback.follow_up")
		if err != nil {
			return Feedback{}, err
		}
		if len(users) > 0 {
			label := req.ContextType
			if req.Label != nil {
				label = *req.Label
			}
			if err := m.Notify.Send(ctx, tx, notify.Message{Event: "crm.feedback_low_score", Category: "feedback", UserIDs: users, PropertyID: &req.PropertyID,
				Data: map[string]any{"context": label, "rating": in.Rating, "comment": in.Comment}, Link: "/crm/feedback/" + fid.String()}); err != nil {
				return Feedback{}, err
			}
		}
	}
	rows, err := tx.Query(ctx, feedbackSelect+` WHERE f.id = $1`, fid)
	f, err := handle.One[Feedback](rows, err, "feedback")
	if err != nil {
		return f, err
	}
	return f, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.feedback", EntityID: fid.String(),
		EntityLabel: req.ContextType, PropertyID: &req.PropertyID, After: f})
}

// ── campaigns (FR-CRM-05) ─────────────────────────────────────────────────

type CampaignResult struct {
	CampaignID uuid.UUID `json:"campaignId"`
	Sent       int       `json:"sent"`
	Skipped    int       `json:"skipped" doc:"No marketing consent or no contact"`
}

// CampaignSender sends a campaign with the P3 rules (per-channel consent,
// suppression, frequency cap, throttling, approval); set by internal/app.
type CampaignSender func(ctx context.Context, tx pgx.Tx, property, cid uuid.UUID) (CampaignResult, error)

var campaignSender CampaignSender

// SetCampaignSender routes P2's "send campaign" through PRD P3 Customer
// Engagement (crm/engagement).
func SetCampaignSender(fn CampaignSender) { campaignSender = fn }

// SendCampaign sends the template to the segment, honouring opt-in.
func (m *Engagement) SendCampaign(ctx context.Context, tx pgx.Tx, property, cid uuid.UUID) (CampaignResult, error) {
	if campaignSender != nil {
		return campaignSender(ctx, tx, property, cid)
	}
	var c struct {
		Code, Event, Channel, Status string
		Segment                      uuid.UUID
		Data                         map[string]any
	}
	if err := tx.QueryRow(ctx, `SELECT code, template_event, channel, status, segment_id, data FROM crm.campaigns WHERE id = $1 AND property_id = $2 FOR UPDATE`,
		cid, property).Scan(&c.Code, &c.Event, &c.Channel, &c.Status, &c.Segment, &c.Data); err != nil {
		if dbtx.IsNoRows(err) {
			return CampaignResult{}, errs.NotFound("campaign")
		}
		return CampaignResult{}, err
	}
	if c.Status != "draft" {
		return CampaignResult{}, errs.Conflict("already_sent", "this campaign has already been sent")
	}
	type rec struct {
		ID      uuid.UUID  `db:"id"`
		Name    string     `db:"name"`
		Email   *string    `db:"email"`
		Phone   *string    `db:"phone"`
		UserID  *uuid.UUID `db:"user_id"`
		Locale  *string    `db:"locale"`
		Consent bool       `db:"marketing_opt_in"`
	}
	recs, err := handle.List[rec](tx.Query(ctx, `SELECT c.id, c.name, c.email, c.phone, c.user_id, c.locale, c.marketing_opt_in FROM crm.segment_members s
		JOIN crm.customers c ON c.id = s.customer_id WHERE s.segment_id = $1`, c.Segment))
	if err != nil {
		return CampaignResult{}, err
	}
	res := CampaignResult{CampaignID: cid}
	for _, r := range recs {
		status := "queued"
		msg := notify.Message{Event: c.Event, Category: "marketing", PropertyID: &property, Data: map[string]any{"name": r.Name}}
		for k, v := range c.Data {
			msg.Data[k] = v
		}
		switch {
		case !r.Consent:
			status = "skipped_no_consent"
		case r.UserID != nil:
			msg.UserIDs, msg.Channels = []uuid.UUID{*r.UserID}, []string{c.Channel}
		case c.Channel == "email" && r.Email != nil:
			msg.Email, msg.Channels = *r.Email, []string{notify.ChannelEmail}
			if r.Locale != nil {
				msg.Locale = *r.Locale
			}
		default:
			status = "skipped_no_contact"
		}
		if status == "queued" && m.Notify != nil {
			if err := m.Notify.Send(ctx, tx, msg); err != nil {
				return res, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO crm.interactions (id, property_id, customer_id, channel, direction, subject, source, ref_type, ref_id)
				VALUES ($1,$2,$3,$4,'outbound',$5,'campaign','crm.campaign',$6)`, id.New(), property, r.ID, c.Channel, "Campaign "+c.Code, cid); err != nil {
				return res, err
			}
			res.Sent++
		} else {
			res.Skipped++
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.campaign_deliveries (campaign_id, property_id, customer_id, status) VALUES ($1,$2,$3,$4)`,
			cid, property, r.ID, status); err != nil {
			return res, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.campaigns SET status = 'sent', sent_at = now(), sent_count = $2, skipped_count = $3 WHERE id = $1`,
		cid, res.Sent, res.Skipped); err != nil {
		return res, err
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "send", EntityType: "crm.campaign", EntityID: cid.String(),
		EntityLabel: c.Code, PropertyID: &property, After: res})
}

// Templates are the CRM notification templates (FR-INT-P2-04).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"crm.feedback_survey": {
			"en": {"How was your visit?", "Hi {{.name}}, thank you for visiting ({{.context}}). Tell us how it went: {{.link}}"},
			"id": {"Bagaimana kunjungan Anda?", "Halo {{.name}}, terima kasih atas kunjungan Anda ({{.context}}). Beri penilaian di sini: {{.link}}"},
		},
		"crm.feedback_low_score": {
			"en": {"Low feedback score: {{.context}}", "A guest rated {{.context}} {{.rating}}/5. Comment: {{.comment}}"},
			"id": {"Skor feedback rendah: {{.context}}", "Tamu memberi nilai {{.rating}}/5 untuk {{.context}}. Komentar: {{.comment}}"},
		},
	}
	var out []provision.Template
	for event, langs := range t {
		for loc, v := range langs {
			for _, ch := range []string{"in_app", "email", "whatsapp"} {
				out = append(out, provision.Template{Event: event, Channel: ch, Locale: loc, Subject: v[0], Body: v[1]})
			}
		}
	}
	return out
}
