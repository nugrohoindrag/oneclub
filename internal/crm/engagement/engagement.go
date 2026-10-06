// Package engagement is Customer Engagement of PRD P3, the P3-owned
// crm/engagement sub-package (PRD P3 §5.4.1): Advanced Customer 360 &
// Segmentation (EP-05), Campaign & Reminder with per-channel consent,
// suppression, frequency cap, tracking and approval (EP-06), Feedback, NPS
// & Complaint Ticket with SLA and escalation (EP-07), the Member App
// Support and Communication Preferences (EP-19) and the Campaign and
// Complaint Policies (EP-22). It extends P2's CRM Foundation (crm root)
// additively and reads other domains through reporting views.
package engagement

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/crm/loyalty"
	"oneclub/internal/crm/topspender"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
)

// Domain events of engagement (PRD P3 §11).
const (
	EventCampaignSent   = "crm.campaign_sent"
	EventTicketCreated  = "crm.ticket_created"
	EventTicketEscalate = "crm.ticket_escalated"
	EventTicketResolved = "crm.ticket_resolved"
	// EventCompensationApproved lets Commercial issue a voucher and Billing a
	// refund for an approved complaint compensation (points are posted here).
	EventCompensationApproved = "crm.ticket_compensation_approved"
)

// TagFunc returns the customers of a property carrying a segmentation tag
// (e.g. tournament_participant, wedding) — contributed by other modules
// through internal/app (FR-C360-02).
type TagFunc func(ctx context.Context, q dbtx.Querier, property uuid.UUID, since time.Time) (map[uuid.UUID]bool, error)

// Service is the engagement service.
type Service struct {
	DB         *dbtx.DB
	Events     crm.Publisher
	Approvals  *approval.Engine
	Notify     notify.Sender
	CRM        *crm.Engagement // P2 CRM: segment compute, Customer 360 sections
	Loyalty    *loyalty.Module
	TopSpender *topspender.Service
	WebsiteURL func() string // public website (tracking & unsubscribe links)
	PortalURL  func() string // Member Portal (ticket & preference links)
	// Tags are segmentation tags of other modules (wired by internal/app).
	Tags map[string]TagFunc
	// CorporateSections are Corporate 360 sections of other modules
	// (banquet events, …), wired by internal/app.
	CorporateSections map[string]crm.SectionFunc
}

// ── Campaign Policies (FR-CMP-03, FR-CMP-07, FR-CMP-08) ──────────────────

// CampaignPolicy is the Campaign Policies club policy.
type CampaignPolicy struct {
	FrequencyCapCount int `json:"frequencyCapCount" doc:"Campaign messages per customer within the window (0 = no cap)"`
	FrequencyCapDays  int `json:"frequencyCapDays"`
	BatchSize         int `json:"batchSize" doc:"Messages queued per dispatch run (throttling to the WhatsApp BSP limits)"`
	ApprovalThreshold int `json:"approvalThreshold" doc:"Campaigns to more recipients need approval (0 = never)"`
	ConversionDays    int `json:"conversionDays" doc:"A payment or promo code use within N days of the message is a conversion"`
}

// DefaultCampaignPolicy: at most 4 campaigns per 30 days, 500 messages per
// run, approval above 1,000 recipients, 7-day conversion window.
var DefaultCampaignPolicy = CampaignPolicy{FrequencyCapCount: 4, FrequencyCapDays: 30, BatchSize: 500, ApprovalThreshold: 1000, ConversionDays: 7}

// SLATarget is the SLA of one priority (business minutes).
type SLATarget struct {
	Priority             string `json:"priority" enum:"low,medium,high,urgent"`
	FirstResponseMinutes int    `json:"firstResponseMinutes"`
	ResolutionMinutes    int    `json:"resolutionMinutes"`
}

// ComplaintPolicy is the Complaint Policies club policy (SLA, escalation,
// reopen, auto-ticket from feedback).
type ComplaintPolicy struct {
	Targets                 []SLATarget       `json:"targets"`
	BusinessHoursStart      string            `json:"businessHoursStart" doc:"HH:MM; SLA timers run in business hours only"`
	BusinessHoursEnd        string            `json:"businessHoursEnd"`
	BusinessDays            []int             `json:"businessDays" doc:"ISO weekdays 1 (Monday) – 7 (Sunday)"`
	SecondEscalationMinutes int               `json:"secondEscalationMinutes" doc:"Still unresolved this long after the first escalation: escalate to the top role"`
	LineManagers            map[string]string `json:"lineManagers" doc:"Role code of the line manager per business line (first escalation)"`
	TopEscalationRole       string            `json:"topEscalationRole"`
	ReopenDays              int               `json:"reopenDays" doc:"The customer may reopen a resolved ticket within N days"`
	AutoCloseDays           int               `json:"autoCloseDays" doc:"Resolved tickets close after N days"`
	AutoTicketFromFeedback  bool              `json:"autoTicketFromFeedback"`
	AutoTicketMaxRating     int               `json:"autoTicketMaxRating" doc:"Feedback rated this or lower opens a ticket"`
	AutoTicketOnDetractor   bool              `json:"autoTicketOnDetractor" doc:"NPS 0–6 also opens a ticket"`
}

// DefaultComplaintPolicy follows PRD P3 §16 #3 business hours (08.00–20.00).
var DefaultComplaintPolicy = ComplaintPolicy{
	Targets: []SLATarget{{Priority: "urgent", FirstResponseMinutes: 30, ResolutionMinutes: 240}, {Priority: "high", FirstResponseMinutes: 60, ResolutionMinutes: 480},
		{Priority: "medium", FirstResponseMinutes: 240, ResolutionMinutes: 1440}, {Priority: "low", FirstResponseMinutes: 480, ResolutionMinutes: 2880}},
	BusinessHoursStart: "08:00", BusinessHoursEnd: "20:00", BusinessDays: []int{1, 2, 3, 4, 5, 6, 7}, SecondEscalationMinutes: 120,
	LineManagers: map[string]string{"golf": "golf_manager", "sportclub": "sport_club_manager", "stay": "resort_manager", "pos": "outlet_manager",
		"banquet": "banquet_manager", "membership": "membership_manager", "other": "club_manager"},
	TopEscalationRole: "general_manager", ReopenDays: 7, AutoCloseDays: 3, AutoTicketFromFeedback: true, AutoTicketMaxRating: 2}

const (
	campaignPolicyCode  = "crm.campaign"
	complaintPolicyCode = "crm.complaint"
)

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: campaignPolicyCode, Category: "Campaign Policies", Name: "Campaign frequency, throttling & approval",
		Description: "Frequency cap per customer, messages per dispatch run, approval above a recipient count, conversion window",
		Default:     DefaultCampaignPolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: complaintPolicyCode, Category: "Complaint Policies", Name: "Complaint SLA & escalation",
		Description: "First response and resolution SLA per priority in business hours, escalation roles, reopen and auto-close, tickets from low feedback",
		Default:     DefaultComplaintPolicy})
	for _, c := range []string{"Campaign Policies", "Complaint Policies"} { // PRD P3 §7.6 / EP-22 proposed labels
		if !slices.Contains(rules.PolicyCategories, c) {
			rules.PolicyCategories = append(rules.PolicyCategories, c)
		}
	}
	extendDefs()
}

func campaignPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (CampaignPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, campaignPolicyCode, property, DefaultCampaignPolicy)
	return p, err
}

func complaintPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (ComplaintPolicy, int, error) {
	p, ref, err := rules.PolicyAt(ctx, q, complaintPolicyCode, property, DefaultComplaintPolicy)
	return p, ref.Version, err
}

// extendDefs adds the P3 fields to P2's segment and campaign resources
// (additive; the generic engine renders and validates them).
func extendDefs() {
	crm.Segments.Fields = append(crm.Segments.Fields,
		resource.Field{Name: "segmentType", Column: "segment_type", Label: "Segment Type", Kind: resource.Enum, Enum: []string{"dynamic", "static"},
			Default: "dynamic", Filter: true},
		resource.Field{Name: "refreshedAt", Column: "refreshed_at", Label: "Refreshed At", Kind: resource.Timestamp, ReadOnly: true})
	for i := range crm.Campaigns.Fields {
		f := &crm.Campaigns.Fields[i]
		switch f.Name {
		case "status":
			f.Enum = []string{"draft", "pending", "scheduled", "sent", "cancelled"}
		case "templateEvent":
			f.Default = CampaignTemplate
		}
	}
	crm.Campaigns.Fields = append(crm.Campaigns.Fields,
		resource.Field{Name: "subject", Column: "subject", Label: "Subject", Kind: resource.String, Max: 200},
		resource.Field{Name: "body", Column: "body", Label: "Message ({{.name}}, {{.promoCode}}, {{.link}})", Kind: resource.Text, Max: 4000},
		resource.Field{Name: "promoCode", Column: "promo_code", Label: "Promo Code (Commercial)", Kind: resource.String, Max: 40, Upper: true},
		resource.Field{Name: "promoMode", Column: "promo_mode", Label: "Promo Code Mode", Kind: resource.Enum, Enum: []string{"none", "shared", "unique"},
			Default: "none"},
		resource.Field{Name: "targetUrl", Column: "target_url", Label: "Link (tracked)", Kind: resource.String, Max: 500},
		resource.Field{Name: "voucherTypeRef", Column: "voucher_type_ref", Label: "Voucher per Recipient (Commercial voucher type code)", Kind: resource.String,
			Max: 40},
		resource.Field{Name: "scheduledAt", Column: "scheduled_at", Label: "Scheduled At", Kind: resource.Timestamp, ReadOnly: true},
		resource.Field{Name: "recipientCount", Column: "recipient_count", Label: "Recipients", Kind: resource.Int, ReadOnly: true},
		resource.Field{Name: "approvalRequestId", Column: "approval_request_id", Label: "Approval Request", Kind: resource.UUID, ReadOnly: true},
		resource.Field{Name: "cancelReason", Column: "cancel_reason", Label: "Cancel Reason", Kind: resource.Text, ReadOnly: true})
	crm.Campaigns.Hooks.BeforeWrite = campaignBeforeWrite
}

// ── helpers ───────────────────────────────────────────────────────────────

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func token() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func shortCode(n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func location(ctx context.Context, q dbtx.Querier, property uuid.UUID) *time.Location {
	l, err := org.Location(ctx, q, property)
	if err != nil || l == nil {
		return time.UTC
	}
	return l
}

// localToday is the calendar date of the property (UTC midnight).
func localToday(ctx context.Context, q dbtx.Querier, property uuid.UUID) time.Time {
	n := clock.Now().In(location(ctx, q, property))
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// render executes a message template ({{.name}} …) with the recipient data;
// a broken template falls back to the raw text.
func render(src string, data map[string]any) string {
	if !strings.Contains(src, "{{") {
		return src
	}
	t, err := template.New("m").Option("missingkey=zero").Parse(src)
	if err != nil {
		return src
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return src
	}
	return strings.ReplaceAll(b.String(), "<no value>", "")
}

func (s *Service) website() string {
	if s.WebsiteURL == nil {
		return ""
	}
	return strings.TrimRight(s.WebsiteURL(), "/")
}

func (s *Service) portal() string {
	if s.PortalURL == nil {
		return ""
	}
	return strings.TrimRight(s.PortalURL(), "/")
}

// roleHolders returns the active users holding a role template at a
// property (escalation recipients).
func roleHolders(ctx context.Context, q dbtx.Querier, property uuid.UUID, role string) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT u.id FROM platform.role_assignments ra JOIN platform.roles r ON r.id = ra.role_id AND r.code = $2
		JOIN platform.users u ON u.id = ra.user_id AND u.status = 'active' WHERE ra.property_id IS NULL OR ra.property_id = $1`, property, role)
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
		out = append(out, u)
	}
	return out, rows.Err()
}

// properties lists the active properties (jobs, system scope).
func (s *Service) properties(ctx context.Context) ([]uuid.UUID, error) {
	var out []uuid.UUID
	err := s.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active' AND archived_at IS NULL ORDER BY id`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	})
	return out, err
}
