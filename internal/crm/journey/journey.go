// Package journey is Campaign & Lifecycle Automation of PRD P5 (EP-19), the
// P5-owned crm/journey sub-package (PRD P5 §5.4.1): multi-step journeys
// with triggers (event, segment entry, date), waits, conditions and
// branches, actions (WhatsApp, e-mail, in-app / Member App offer, voucher,
// points, reward, sales task, tag), exit rules, A/B messages and a control
// group, enrollment per customer and occurrence, and the performance per
// journey and step (sent, read, clicked, converted, attributed revenue).
//
// Every message step applies the P3 consent and suppression rules
// (engagement.Addressee) and, for marketing journeys, the frequency cap
// (2 per week, 6 per month across campaigns and journeys) and the quiet
// hours (21.00–08.00) of the Marketing Frequency policy (PRD P5 §16 #15);
// transactional journeys are not counted and not deferred.
package journey

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/crm/loyalty"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/rules"
)

// EventStepExecuted is published for every executed journey step (PRD P5 §11).
const EventStepExecuted = "crm.journey_step_executed"

// MessageTemplate is the notification template of journey messages (the
// subject and body come from the step).
const MessageTemplate = "crm.journey_message"

// ActivationDocumentType is the approval of a journey before it goes live
// (PRD P5 §15 #7: approval journey; no workflow ⇒ approved at once).
var ActivationDocumentType = provision.DocumentType{Code: "journey_activation", Module: "crm", Name: "Journey Activation",
	Attributes: []provision.DocumentAttribute{{Key: "category", Label: "Category", Type: "string"}, {Key: "messages", Label: "Message steps", Type: "number"}}}

// TriggerEvents are the domain events a journey can start on, exit on or
// count as its goal (the customer is resolved from the payload).
var TriggerEvents = []string{"membership.activated", "membership.renewed", "banquet.event_completed", "banquet.event_confirmed",
	"billing.payment_settled", "golf.booking_confirmed", "golf.booking_cancelled", "golf.round_finished", "crm.tier_changed", "crm.ticket_resolved",
	"commercial.package_booked"}

// Enumerations.
var (
	TemplateKinds  = []string{"renewal", "birthday", "welcome", "win_back", "post_event", "abandoned_booking"}
	TriggerTypes   = []string{"event", "segment_entry", "date", "manual"}
	TriggerDates   = []string{"birthday", "membership_expiry", "last_visit"}
	StepTypes      = []string{"message", "wait", "condition", "voucher", "points", "reward", "sales_task", "tag", "exit"}
	ConditionKinds = []string{"booked", "paid", "renewed", "in_segment", "tier", "clicked", "opted_in"}
	Channels       = []string{"email", "whatsapp", "in_app"}
)

// Special step targets.
const (
	TargetExit = "exit" // leave the journey (exited)
	TargetEnd  = "end"  // finish the journey (completed)
)

// Service is the journey service.
type Service struct {
	DB        *dbtx.DB
	Events    crm.Publisher
	Approvals *approval.Engine
	Notify    notify.Sender
	Loyalty   *loyalty.Module
	// IssueVoucher issues Commercial vouchers of a voucher step (wired by
	// internal/app: crm cannot import commercial); idempotent per source.
	IssueVoucher func(ctx context.Context, tx pgx.Tx, in VoucherRequest) ([]string, error)
	WebsiteURL   func() string
	PortalURL    func() string
}

// VoucherRequest asks Commercial for the vouchers of a journey step.
type VoucherRequest struct {
	PropertyID uuid.UUID
	TypeCode   string
	CustomerID uuid.UUID
	SourceID   uuid.UUID // journey event
	Reference  string
}

// ── Marketing Frequency policy (PRD P5 §16 #15) ───────────────────────────

// Policy is the "Marketing frequency & quiet hours" club policy.
type Policy struct {
	WeeklyCap        int    `json:"weeklyCap" doc:"Marketing messages per customer in 7 days (campaigns + journeys; 0 = no cap)"`
	MonthlyCap       int    `json:"monthlyCap" doc:"Marketing messages per customer in 30 days (0 = no cap)"`
	QuietStart       string `json:"quietStart" doc:"HH:MM — marketing messages wait until the quiet hours end"`
	QuietEnd         string `json:"quietEnd" doc:"HH:MM"`
	CountCampaigns   bool   `json:"countCampaigns" doc:"Campaign messages count toward the journey cap"`
	BatchSize        int    `json:"batchSize" doc:"Enrollments processed per journey and run"`
	ActivationReview bool   `json:"activationReview" doc:"Activating a journey goes through the approval engine (Journey Activation)"`
}

// DefaultPolicy: 2 per week, 6 per month, quiet 21.00–08.00.
var DefaultPolicy = Policy{WeeklyCap: 2, MonthlyCap: 6, QuietStart: "21:00", QuietEnd: "08:00", CountCampaigns: true, BatchSize: 500, ActivationReview: true}

// PolicyCode is the club policy code.
const PolicyCode = "crm.journey"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCode, Category: "Campaign Policies", Name: "Marketing frequency & quiet hours (journeys)",
		Description: "Frequency cap per customer per week and month across campaigns and journeys, quiet hours, journey batch size and activation approval",
		Default:     DefaultPolicy})
}

// LoadPolicy returns the policy in force.
func LoadPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (Policy, error) {
	p, _, err := rules.PolicyAt(ctx, q, PolicyCode, property, DefaultPolicy)
	if p.BatchSize <= 0 {
		p.BatchSize = 500
	}
	return p, err
}

// ── model ─────────────────────────────────────────────────────────────────

// Journey is a journey with its steps.
type Journey struct {
	ID                uuid.UUID     `json:"id" db:"id"`
	Code              string        `json:"code" db:"code"`
	Name              string        `json:"name" db:"name"`
	Description       *string       `json:"description" db:"description"`
	Template          string        `json:"template" db:"template" enum:"custom,renewal,birthday,welcome,win_back,post_event,abandoned_booking"`
	Category          string        `json:"category" db:"category" enum:"marketing,transactional"`
	TriggerType       string        `json:"triggerType" db:"trigger_type" enum:"event,segment_entry,date,manual"`
	TriggerEvent      *string       `json:"triggerEvent" db:"trigger_event"`
	TriggerSegmentID  *uuid.UUID    `json:"triggerSegmentId" db:"trigger_segment_id"`
	TriggerDate       *string       `json:"triggerDate" db:"trigger_date" enum:"birthday,membership_expiry,last_visit"`
	TriggerDays       int           `json:"triggerDays" db:"trigger_days"`
	ExitEvents        []string      `json:"exitEvents" db:"exit_events"`
	ExitSegmentID     *uuid.UUID    `json:"exitSegmentId" db:"exit_segment_id"`
	GoalEvent         *string       `json:"goalEvent" db:"goal_event"`
	GoalDays          int           `json:"goalDays" db:"goal_days"`
	ReEntryDays       int           `json:"reEntryDays" db:"re_entry_days"`
	ControlPercent    int           `json:"controlPercent" db:"control_percent"`
	Status            string        `json:"status" db:"status" enum:"draft,pending,active,paused,completed"`
	ApprovalRequestID *uuid.UUID    `json:"approvalRequestId" db:"approval_request_id"`
	ActivatedAt       *time.Time    `json:"activatedAt" db:"activated_at"`
	PausedAt          *time.Time    `json:"pausedAt" db:"paused_at"`
	CompletedAt       *time.Time    `json:"completedAt" db:"completed_at"`
	LastRunAt         *time.Time    `json:"lastRunAt" db:"last_run_at"`
	CreatedAt         time.Time     `json:"createdAt" db:"created_at"`
	UpdatedAt         time.Time     `json:"updatedAt" db:"updated_at"`
	PropertyID        uuid.UUID     `json:"-" db:"property_id"`
	Steps             []JourneyStep `json:"steps" db:"-"`
	Enrolled          int           `json:"enrolled" db:"enrolled"`
	ActiveEnrollments int           `json:"activeEnrollments" db:"active_enrollments"`
}

// JourneyStep is one step (input and output).
type JourneyStep struct {
	Key             string     `json:"key" db:"key" doc:"Unique in the journey: a-z, 0-9, _"`
	Position        int        `json:"position,omitempty" db:"position" doc:"Order (default: list order)"`
	StepType        string     `json:"stepType" db:"step_type" enum:"message,wait,condition,voucher,points,reward,sales_task,tag,exit"`
	Name            string     `json:"name" db:"name"`
	Channel         *string    `json:"channel,omitempty" db:"channel" enum:"email,whatsapp,in_app"`
	Subject         *string    `json:"subject,omitempty" db:"subject"`
	Body            *string    `json:"body,omitempty" db:"body" doc:"{{.name}}, {{.date}}, {{.detail}}, {{.promoCode}}, {{.offer}}, {{.link}}"`
	SubjectB        *string    `json:"subjectB,omitempty" db:"subject_b" doc:"A/B test: subject of variant B"`
	BodyB           *string    `json:"bodyB,omitempty" db:"body_b" doc:"A/B test: message of variant B"`
	SplitPercent    int        `json:"splitPercent,omitempty" db:"split_percent" doc:"A/B test: % of recipients who get variant B"`
	OfferTitle      *string    `json:"offerTitle,omitempty" db:"offer_title" doc:"Shown in Member App → Offers"`
	PromoCode       *string    `json:"promoCode,omitempty" db:"promo_code"`
	OfferValidDays  *int       `json:"offerValidDays,omitempty" db:"offer_valid_days"`
	WaitDays        *int       `json:"waitDays,omitempty" db:"wait_days"`
	WaitHours       *int       `json:"waitHours,omitempty" db:"wait_hours"`
	UntilDaysBefore *int       `json:"untilDaysBefore,omitempty" db:"until_days_before" doc:"Wait until N days before the anchor date (renewal: end of membership; birthday)"`
	ConditionKind   *string    `json:"conditionKind,omitempty" db:"condition_kind" enum:"booked,paid,renewed,in_segment,tier,clicked,opted_in"`
	ConditionValue  *string    `json:"conditionValue,omitempty" db:"condition_value" doc:"in_segment: segment id; tier: tier codes (comma separated); opted_in: channel"`
	OnTrue          *string    `json:"onTrue,omitempty" db:"on_true" doc:"Step key, exit or end (default: next step)"`
	OnFalse         *string    `json:"onFalse,omitempty" db:"on_false"`
	NextKey         *string    `json:"nextKey,omitempty" db:"next_key"`
	Points          *int64     `json:"points,omitempty" db:"points"`
	VoucherTypeRef  *string    `json:"voucherTypeRef,omitempty" db:"voucher_type_ref"`
	RewardID        *uuid.UUID `json:"rewardId,omitempty" db:"reward_id"`
	TaskSubject     *string    `json:"taskSubject,omitempty" db:"task_subject"`
	TaskDueDays     *int       `json:"taskDueDays,omitempty" db:"task_due_days"`
	Tag             *string    `json:"tag,omitempty" db:"tag"`
}

// JourneyInput creates or replaces a journey (steps included).
type JourneyInput struct {
	Code             string        `json:"code"`
	Name             string        `json:"name"`
	Description      string        `json:"description,omitempty"`
	Category         string        `json:"category,omitempty" enum:"marketing,transactional"`
	TriggerType      string        `json:"triggerType" enum:"event,segment_entry,date,manual"`
	TriggerEvent     string        `json:"triggerEvent,omitempty"`
	TriggerSegmentID *uuid.UUID    `json:"triggerSegmentId,omitempty"`
	TriggerDate      string        `json:"triggerDate,omitempty" enum:"birthday,membership_expiry,last_visit"`
	TriggerDays      int           `json:"triggerDays,omitempty" doc:"date: birthday / membership end within N days ahead; last visit N days ago"`
	ExitEvents       []string      `json:"exitEvents,omitempty"`
	ExitSegmentID    *uuid.UUID    `json:"exitSegmentId,omitempty"`
	GoalEvent        string        `json:"goalEvent,omitempty" doc:"Conversion event (e.g. membership.renewed, billing.payment_settled)"`
	GoalDays         int           `json:"goalDays,omitempty"`
	ReEntryDays      int           `json:"reEntryDays,omitempty" doc:"A customer may enter again after N days (0: once per occurrence)"`
	ControlPercent   int           `json:"controlPercent,omitempty" doc:"Control group (no messages or incentives), 0–50%"`
	Steps            []JourneyStep `json:"steps"`
}

var (
	keyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	tagRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)
)

func strp(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func intp(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// Validate checks a journey definition (field errors on the input).
func Validate(in *JourneyInput) error {
	var fe []errs.FieldError
	bad := func(field, code, msg string) { fe = append(fe, errs.Field(field, code, msg)) }
	if strings.TrimSpace(in.Code) == "" {
		bad("code", "required", "code is required")
	}
	if strings.TrimSpace(in.Name) == "" {
		bad("name", "required", "name is required")
	}
	if in.Category == "" {
		in.Category = "marketing"
	}
	if in.Category != "marketing" && in.Category != "transactional" {
		bad("category", "invalid", "marketing or transactional")
	}
	if in.GoalDays == 0 {
		in.GoalDays = 7
	}
	if in.GoalDays < 1 || in.GoalDays > 365 {
		bad("goalDays", "invalid", "1–365 days")
	}
	if in.ControlPercent < 0 || in.ControlPercent > 50 {
		bad("controlPercent", "invalid", "0–50%")
	}
	if in.ReEntryDays < 0 {
		bad("reEntryDays", "invalid", "0 or more days")
	}
	switch in.TriggerType {
	case "event":
		if !slices.Contains(TriggerEvents, in.TriggerEvent) {
			bad("triggerEvent", "invalid_event", "choose one of the supported events")
		}
	case "segment_entry":
		if in.TriggerSegmentID == nil {
			bad("triggerSegmentId", "required", "choose the segment")
		}
	case "date":
		if !slices.Contains(TriggerDates, in.TriggerDate) {
			bad("triggerDate", "invalid", "birthday, membership_expiry or last_visit")
		}
		if in.TriggerDays < 0 || in.TriggerDays > 730 {
			bad("triggerDays", "invalid", "0–730 days")
		}
		if in.TriggerDate == "last_visit" && in.TriggerDays == 0 {
			bad("triggerDays", "required", "days without a visit")
		}
	case "manual":
	default:
		bad("triggerType", "invalid", "event, segment_entry, date or manual")
	}
	for i, ev := range in.ExitEvents {
		if !slices.Contains(TriggerEvents, ev) {
			bad("exitEvents["+itoa(i)+"]", "invalid_event", "unsupported event")
		}
	}
	if in.GoalEvent != "" && !slices.Contains(TriggerEvents, in.GoalEvent) {
		bad("goalEvent", "invalid_event", "unsupported event")
	}
	if len(in.Steps) == 0 {
		bad("steps", "required", "add at least one step")
	}
	keys := map[string]bool{}
	for i := range in.Steps {
		s := &in.Steps[i]
		if !keyRe.MatchString(s.Key) || s.Key == TargetExit || s.Key == TargetEnd {
			bad(stepField(i, "key"), "invalid_key", "a-z, 0-9 and _ (not exit / end)")
		} else if keys[s.Key] {
			bad(stepField(i, "key"), "duplicate", "keys must be unique")
		}
		keys[s.Key] = true
		if s.Position == 0 {
			s.Position = (i + 1) * 10
		}
		if strings.TrimSpace(s.Name) == "" {
			s.Name = s.Key
		}
	}
	anchored := in.TriggerType == "date" && (in.TriggerDate == "membership_expiry" || in.TriggerDate == "birthday")
	target := func(i int, field string, v *string) {
		t := strp(v)
		if t != "" && t != TargetExit && t != TargetEnd && !keys[t] {
			bad(stepField(i, field), "unknown_step", "no step "+t)
		}
	}
	for i := range in.Steps {
		s := &in.Steps[i]
		switch s.StepType {
		case "message":
			if !slices.Contains(Channels, strp(s.Channel)) {
				bad(stepField(i, "channel"), "required", "email, whatsapp or in_app")
			}
			if strp(s.Body) == "" {
				bad(stepField(i, "body"), "required", "the message is required")
			}
			if s.SplitPercent < 0 || s.SplitPercent > 100 {
				bad(stepField(i, "splitPercent"), "invalid", "0–100%")
			}
			if s.SplitPercent > 0 && strp(s.BodyB) == "" {
				bad(stepField(i, "bodyB"), "required", "variant B needs its message")
			}
		case "wait":
			if s.UntilDaysBefore != nil {
				if !anchored {
					bad(stepField(i, "untilDaysBefore"), "not_anchored", "only for membership expiry and birthday journeys")
				}
			} else if intp(s.WaitDays) <= 0 && intp(s.WaitHours) <= 0 {
				bad(stepField(i, "waitDays"), "required", "wait days or hours")
			}
		case "condition":
			k := strp(s.ConditionKind)
			if !slices.Contains(ConditionKinds, k) {
				bad(stepField(i, "conditionKind"), "required", "choose the condition")
			}
			if (k == "in_segment" || k == "tier" || k == "opted_in") && strp(s.ConditionValue) == "" {
				bad(stepField(i, "conditionValue"), "required", "the condition needs a value")
			}
			if k == "in_segment" {
				if _, err := uuid.Parse(strp(s.ConditionValue)); err != nil {
					bad(stepField(i, "conditionValue"), "invalid_uuid", "segment id")
				}
			}
			target(i, "onTrue", s.OnTrue)
			target(i, "onFalse", s.OnFalse)
		case "points":
			if s.Points == nil || *s.Points <= 0 {
				bad(stepField(i, "points"), "required", "points above 0")
			}
		case "voucher":
			if strp(s.VoucherTypeRef) == "" {
				bad(stepField(i, "voucherTypeRef"), "required", "Commercial voucher type code")
			}
		case "reward":
			if s.RewardID == nil {
				bad(stepField(i, "rewardId"), "required", "choose the reward")
			}
		case "sales_task":
			if strp(s.TaskSubject) == "" {
				bad(stepField(i, "taskSubject"), "required", "task subject")
			}
		case "tag":
			if !tagRe.MatchString(strp(s.Tag)) {
				bad(stepField(i, "tag"), "invalid_tag", "a-z, 0-9 and _")
			}
		case "exit":
		default:
			bad(stepField(i, "stepType"), "invalid", "unknown step type")
		}
		target(i, "nextKey", s.NextKey)
	}
	if len(fe) > 0 {
		return errs.Validation("invalid_journey", "the journey is not valid", fe...)
	}
	return nil
}

func stepField(i int, f string) string { return "steps[" + itoa(i) + "]." + f }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
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

func location(ctx context.Context, q dbtx.Querier, property uuid.UUID) *time.Location {
	l, err := org.Location(ctx, q, property)
	if err != nil || l == nil {
		return time.UTC
	}
	return l
}

// localDay is the calendar date of t at the property (UTC midnight).
func localDay(t time.Time, loc *time.Location) time.Time {
	n := t.In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func now() time.Time { return clock.Now() }

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

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
