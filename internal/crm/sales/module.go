// Package sales is CRM Sales of PRD P3 (EP-01 Lead Management, EP-02 Sales
// Pipeline & Opportunity, EP-03 Quotation & Conversion, EP-04 Sales Target &
// Commission), the P3-owned crm/sales sub-package (PRD P3 §5.4.1). It sits in
// the customer layer: business lines (banquet, golf/tournament) create their
// bookings from the crm.quotation_accepted event (docs/p3-p4-contracts.md),
// and commission is recognised from billing events decoded by name — crm
// never imports billing or the business lines (Technical Doc §4.2 #3).
package sales

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/rules"
)

// Domain events published through the outbox (PRD P3 §11).
const (
	EventLeadCreated        = "crm.lead_created"
	EventLeadAssigned       = "crm.lead_assigned"
	EventLeadEscalated      = "crm.lead_escalated"
	EventLeadConverted      = "crm.lead_converted"
	EventOpportunityWon     = "crm.opportunity_won"
	EventOpportunityLost    = "crm.opportunity_lost"
	EventQuotationSent      = "crm.quotation_sent"
	EventQuotationAccepted  = "crm.quotation_accepted"
	EventQuotationRejected  = "crm.quotation_rejected"
	EventQuotationExpired   = "crm.quotation_expired"
	EventCommissionApproved = "crm.commission_approved"
)

// Billing events the commission engine consumes (decoded by name).
const (
	BillingInvoicePaid    = "billing.invoice_paid"
	BillingPaymentSettled = "billing.payment_settled"
	BillingRefund         = "billing.refund_processed"
	BillingInvoiceVoided  = "billing.invoice_voided"
	WebhookReceived       = "integration.webhook_received"
)

// Lines are the business lines of leads, opportunities and quotations (the
// line values of crm.quotation_accepted).
var Lines = []string{"wedding", "banquet", "mice", "event", "tournament", "stay", "golf", "package", "membership", "other"}

// Sources of a lead (roadmap §40.1 + e-mail and phone, PRD P3 EP-01).
var Sources = []string{"whatsapp", "instagram", "facebook", "tiktok", "website_form", "walk_in", "member_referral", "email", "phone", "event",
	"import", "other"}

// EventTypes of an event-like deal (crm.quotation_accepted eventType).
var EventTypes = []string{"wedding", "meeting", "conference", "gathering", "birthday", "tournament", "other"}

// Publisher publishes domain events (outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// Module is the CRM Sales service.
type Module struct {
	DB         *dbtx.DB
	Events     Publisher
	Approvals  *approval.Engine
	Notify     notify.Sender
	Price      Pricer        // price & tax hook (internal/app wires Commercial pricing); nil = manual prices, no tax
	WebsiteURL func() string // public website base (quotation links)
	StaffURL   func() string // Staff App base (deep links in notifications)
	// Integrations resolves e-Meterai and tells whether WhatsApp is
	// configured for acceptance codes (nil: neither).
	Integrations *integration.Service
	// OTPKey peppers the hashes of quotation acceptance codes (the instance
	// application secret).
	OTPKey []byte
}

// Document types of the approval engine.
var (
	DiscountDocumentType = provision.DocumentType{Code: "sales_quotation_discount", Module: "crm", Name: "Quotation Discount",
		Attributes: []provision.DocumentAttribute{{Key: "discountPercent", Label: "Discount %", Type: "number"},
			{Key: "total", Label: "Quotation Total", Type: "number"}, {Key: "line", Label: "Business Line", Type: "string"}}}
	StatementDocumentType = provision.DocumentType{Code: "sales_commission_statement", Module: "crm", Name: "Commission Statement",
		Attributes: []provision.DocumentAttribute{{Key: "total", Label: "Commission Total", Type: "number"}}}
)

// DocumentTypes are the approval document types of CRM Sales.
func DocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{DiscountDocumentType, StatementDocumentType}
}

// ── Sales Policies (FR-POL-P3-03, label proposed in PRD P3 §7.6) ─────────

// PaymentTermRule is a default payment term of a quotation.
type PaymentTermRule struct {
	Label           string `json:"label"`
	Percent         string `json:"percent"`
	DueDays         *int   `json:"dueDays,omitempty" doc:"Due N days after acceptance"`
	DaysBeforeEvent *int   `json:"daysBeforeEvent,omitempty" doc:"Due N days before the event date"`
}

// SalesPolicy is the Sales Policies document.
type SalesPolicy struct {
	AssignmentMode        string            `json:"assignmentMode" doc:"round_robin | fixed | manual (PRD P3 §16 #3)"`
	FixedAssignees        map[string]string `json:"fixedAssignees" doc:"Business line → sales user id (fixed mode; falls back to round-robin)"`
	FirstResponseMinutes  int               `json:"firstResponseMinutes" doc:"First-response SLA within business hours"`
	LineResponseMinutes   map[string]int    `json:"lineResponseMinutes" doc:"SLA override per business line"`
	SLAReminderMinutes    int               `json:"slaReminderMinutes" doc:"Remind the assigned sales this many minutes before the SLA ends"`
	BusinessHoursStart    string            `json:"businessHoursStart" doc:"HH:MM"`
	BusinessHoursEnd      string            `json:"businessHoursEnd" doc:"HH:MM"`
	AfterHoursDueTime     string            `json:"afterHoursDueTime" doc:"Leads outside business hours are due at this time (HH:MM) the next morning"`
	QuotationValidityDays int               `json:"quotationValidityDays"`
	OptionDays            int               `json:"optionDays" doc:"Option date of a tentative venue hold = today + N days"`
	MaxDiscountPercent    string            `json:"maxDiscountPercent" doc:"A quotation discount above this needs approval"`
	RoleDiscountLimits    map[string]string `json:"roleDiscountLimits" doc:"Role code → own discount limit (%) — the highest limit of the sender applies"`
	PricingMode           string            `json:"pricingMode" doc:"nett | plus_plus for manually priced lines"`
	TaxCodes              []string          `json:"taxCodes" doc:"Tax & service rule codes applied to manually priced lines (empty: prices are final)"`
	DefaultPaymentTerms   []PaymentTermRule `json:"defaultPaymentTerms"`
	QuotationTerms        string            `json:"quotationTerms" doc:"Terms & conditions printed on quotations"`
	CommissionRates       map[string]string `json:"commissionRates" doc:"Default commission % per business line (no commission scheme)"`
	CommissionBasis       string            `json:"commissionBasis" doc:"net (before tax & service) | total"`
	ClawbackDays          int               `json:"clawbackDays" doc:"Refunds within N days after the deal was paid claw the commission back"`
	LeadRetentionDays     int               `json:"leadRetentionDays" doc:"Leads that never became customers are anonymised after N days without activity"`
	EmailLines            map[string]string `json:"emailLines" doc:"Department mailbox (local part) → business line of e-mail leads"`
	WhatsAppIntegrations  []string          `json:"whatsAppIntegrations" doc:"Messaging integration codes whose inbound messages become leads of this property"`
	RequireAcceptanceOtp  bool              `json:"requireAcceptanceOtp" doc:"Acceptance on the public link needs a one-time code sent to the customer contact on file (PRD P3 §16 #18)"`
	OtpTTLMinutes         int               `json:"otpTtlMinutes" doc:"Validity of an acceptance code in minutes"`
	OtpMaxAttempts        int               `json:"otpMaxAttempts" doc:"Wrong entries before an acceptance code is locked"`
	EMeteraiThreshold     string            `json:"eMeteraiThreshold" doc:"Quotations / contracts with a total above this amount carry an e-Meterai"`
	EMeteraiOnAcceptance  bool              `json:"eMeteraiOnAcceptance" doc:"Stamp the e-Meterai automatically on acceptance (otherwise staff stamps it)"`
}

func intp(n int) *int { return &n }

// NewDefaultPolicy returns the Sales Policies defaults (PRD P3 §16 #3, #4,
// #5 and the EP-03 AC). A fresh value is built on every call: decoding a
// configured policy into shared maps or slices would change the defaults
// of every property.
func NewDefaultPolicy() SalesPolicy {
	return SalesPolicy{
		AssignmentMode: "round_robin", FixedAssignees: map[string]string{}, FirstResponseMinutes: 60, LineResponseMinutes: map[string]int{},
		SLAReminderMinutes: 15, BusinessHoursStart: "08:00", BusinessHoursEnd: "20:00", AfterHoursDueTime: "09:00",
		QuotationValidityDays: 14, OptionDays: 7, MaxDiscountPercent: "10", RoleDiscountLimits: map[string]string{}, PricingMode: "plus_plus",
		TaxCodes: []string{},
		DefaultPaymentTerms: []PaymentTermRule{{Label: "Down Payment 30%", Percent: "30", DueDays: intp(7)},
			{Label: "Final Payment", Percent: "70", DaysBeforeEvent: intp(7)}},
		QuotationTerms: "Prices are valid until the validity date. The down payment confirms the booking; the balance is due before the event.",
		CommissionRates: map[string]string{"wedding": "1", "banquet": "1", "mice": "1", "event": "1", "membership": "3", "golf": "2",
			"tournament": "2", "stay": "1", "package": "1", "other": "1"},
		CommissionBasis: "net", ClawbackDays: 90, LeadRetentionDays: 730,
		EmailLines: map[string]string{"wedding": "wedding", "banquet": "banquet", "mice": "mice", "events": "event", "golf": "golf",
			"membership": "membership", "reservation": "stay", "marketing": "other", "sales": "other"},
		WhatsAppIntegrations: []string{},
		RequireAcceptanceOtp: true, OtpTTLMinutes: 10, OtpMaxAttempts: 5, EMeteraiThreshold: "5000000", EMeteraiOnAcceptance: true,
	}
}

// PolicyCode is the Sales Policies code in Settings → Club Policies.
const PolicyCode = "crm.sales"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCode, Category: "Sales Policies", Name: "Leads, quotations & commission",
		Description: "Lead assignment and first-response SLA, quotation validity, discount approval limits, payment terms, tax & service " +
			"of manual prices, default commission rates, the acceptance code (OTP) and the e-Meterai threshold", Default: NewDefaultPolicy()})
}

// LoadPolicy returns the Sales Policies version in force at the property.
func LoadPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (SalesPolicy, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, PolicyCode, property, NewDefaultPolicy())
}

// ── helpers ───────────────────────────────────────────────────────────────

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Zero
	}
	return d
}

func places(cur string) int32 {
	if cur == "IDR" || cur == "JPY" || cur == "" {
		return 0
	}
	return 2
}

func round(d decimal.Decimal, cur string) decimal.Decimal { return d.Round(places(cur)) }

var hundred = decimal.NewFromInt(100)

func nullStr(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
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

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		u := p.UserID
		return &u
	}
	return nil
}

func can(ctx context.Context, perm string, property uuid.UUID) bool {
	p := authz.From(ctx)
	return p != nil && p.Can(perm, &property)
}

func oneOf(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func enumErr(field string, list []string) error {
	return handle.Invalid(field, "invalid", "one of: "+strings.Join(list, ", "))
}

// location returns the timezone of the property.
func location(ctx context.Context, q dbtx.Querier, property uuid.UUID) *time.Location {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}

// localToday is the calendar date of the property (UTC midnight).
func localToday(ctx context.Context, q dbtx.Querier, property uuid.UUID) time.Time {
	l := clock.Now().In(location(ctx, q, property))
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

func parseDate(field, v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, handle.Invalid(field, "invalid_date", "must be a date (YYYY-MM-DD)")
	}
	return &t, nil
}

// yearlyNumber issues PREFIX-YYYY-NNNNN, gap-free per property and year
// (quotation numbers of the contract, e.g. QUO-2026-00012).
func yearlyNumber(ctx context.Context, tx pgx.Tx, property uuid.UUID, prefix string, year int) (string, error) {
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO crm.sales_sequences (property_id, prefix, year, last_value) VALUES ($1, $2, $3, 1)
		ON CONFLICT (property_id, prefix, year) DO UPDATE SET last_value = crm.sales_sequences.last_value + 1 RETURNING last_value`,
		property, prefix, year).Scan(&n)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d-%05d", prefix, year, n), nil
}

// staffLink is a Back Office deep link.
func (m *Module) staffLink(path string) string {
	if m.StaffURL == nil {
		return path
	}
	return strings.TrimRight(m.StaffURL(), "/") + path
}

// publicLink is the website link of a quotation token.
func (m *Module) publicLink(token string) string {
	base := ""
	if m.WebsiteURL != nil {
		base = strings.TrimRight(m.WebsiteURL(), "/")
	}
	return base + "/id/quotation/" + token
}

// notifyUsers sends an in-app + e-mail notification to staff users.
func (m *Module) notifyUsers(ctx context.Context, tx pgx.Tx, property uuid.UUID, users []uuid.UUID, event, link string, data map[string]any) error {
	if m.Notify == nil || len(users) == 0 {
		return nil
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "crm", UserIDs: users, Link: link, PropertyID: &property, Data: data})
}

func conflict(code, msg string) error { return errs.Conflict(code, msg) }

// ensureUser checks that a user exists and is active.
func ensureUser(ctx context.Context, q dbtx.Querier, field string, uid uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.users WHERE id = $1 AND status = 'active')`, uid).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return handle.Invalid(field, "not_found", "user not found or inactive")
	}
	return nil
}

// holders returns the users holding a permission at the property.
func holders(ctx context.Context, q dbtx.Querier, property uuid.UUID, perm string) []uuid.UUID {
	us, err := notify.Holders(ctx, q, property, perm)
	if err != nil {
		return nil
	}
	return us
}

// getOne adapts handle.One to a query result: getOne[T]("lead")(q.Query(…)).
func getOne[T any](what string) func(pgx.Rows, error) (T, error) {
	return func(rows pgx.Rows, err error) (T, error) { return handle.One[T](rows, err, what) }
}
