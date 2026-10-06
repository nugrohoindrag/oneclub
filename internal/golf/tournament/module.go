// Package tournament is PRD P3 EP-16 Tournament Management: the P3-owned
// sub-package golf/tournament of the golf module (PRD P3 §5.4.1). It uses
// P1 golf through the golf package's public API (playing routes, course
// blocks on the tee sheet) and the P2 golf experience through its public
// interface (digital scorecards, handicap, Hall of Fame) — it never writes
// P1 or P2 tables. The banquet event of a tournament (venue, catering) is a
// plain event id; corporate tournaments arrive through crm.quotation_accepted.
//
//	Create → Registration & Fee → Flighting / Shotgun Draw → Scoring (P2 scorecard)
//	→ Live Leaderboard → Finalize → Prize & Hall of Fame → Tournament Report
package tournament

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/golf"
	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/realtime"
)

// Domain events (PRD P3 §11; contract golf.tournament_results_published in
// docs/p3-p4-contracts.md).
const (
	EventRegistrationConfirmed = "golf.tournament_registration_confirmed"
	EventStarted               = "golf.tournament_started"
	EventFinalized             = "golf.tournament_finalized"
	EventResultsPublished      = "golf.tournament_results_published"
	// EventQuotationAccepted is consumed: line "tournament" → draft tournament.
	EventQuotationAccepted = "crm.quotation_accepted"
)

// Topic is the realtime topic of tournament screens (Tournament Desk,
// leaderboards, Leaderboard Screen): data carries the tournament id only.
const Topic = "golf.tournament"

// FeeWaiverType approves a complimentary entry (waived tournament fee) for
// an invited player or a sponsor guest.
var FeeWaiverType = provision.DocumentType{Code: "golf_tournament_fee_waiver", Module: "golf", Name: "Tournament Fee Waiver",
	Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Fee amount", Type: "number"}}}

// DocumentTypes are the approval document types of tournaments.
func DocumentTypes() []provision.DocumentType { return []provision.DocumentType{FeeWaiverType} }

// Invoicer issues billing invoices (sponsor billing to corporate accounts);
// implemented by billing.HTTP.
type Invoicer interface {
	CreateInvoice(ctx context.Context, tx pgx.Tx, property uuid.UUID, in billing.InvoiceInput) (billing.InvoiceDetail, error)
}

// Module is the tournament service.
type Module struct {
	DB           *dbtx.DB
	Events       golf.Publisher
	Approvals    *approval.Engine
	Billing      *billing.Service
	Invoices     Invoicer         // sponsor invoices
	Refunds      billing.Approver // refunds above the Refund Policy limit
	Notify       notify.Sender
	Hub          *realtime.Hub
	Golf         *golf.Module         // course blocks, playing routes (P1 public API)
	Experience   *experience.Module   // scorecards, handicap, Hall of Fame (P2 public API)
	Integrations *integration.Service // CAPTCHA of the website registration
	Cfg          *config.Config
}

// ── helpers ───────────────────────────────────────────────────────────────

var hundred = decimal.NewFromInt(100)

func dec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(strings.TrimSpace(s))
	return d
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	s = strings.TrimSpace(s)
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptr[T any](v T) *T { return &v }

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func can(ctx context.Context, perm string, property uuid.UUID) bool {
	p := authz.From(ctx)
	return p != nil && p.Can(perm, &property)
}

func location(ctx context.Context, q dbtx.Querier, property uuid.UUID) *time.Location {
	if loc, err := org.Location(ctx, q, property); err == nil && loc != nil {
		return loc
	}
	return time.UTC
}

// localDate is the club-local calendar date of t (as a UTC midnight value).
func localDate(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// atLocal is HH:MM on a local date.
func atLocal(day time.Time, hhmm string, loc *time.Location) time.Time {
	t, _ := time.Parse("15:04", hhmm)
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, loc)
}

func places(cur string) int32 {
	if cur == "IDR" || cur == "JPY" {
		return 0
	}
	return 2
}

// money formats an amount for notifications (IDR 1.500.000).
func money(d decimal.Decimal, cur string) string {
	s := d.StringFixed(places(cur))
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if frac != "" {
		out += "," + frac
	}
	if neg {
		out = "-" + out
	}
	return cur + " " + out
}

func record(ctx context.Context, tx pgx.Tx, entity string, eid uuid.UUID, label, action string, property uuid.UUID, before, after any, reason string) error {
	return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: action, EntityType: entity, EntityID: eid.String(), EntityLabel: label,
		PropertyID: &property, Before: before, After: after, Reason: reason})
}

func (m *Module) publish(ctx context.Context, tx pgx.Tx, event, aggregate string, aid, property uuid.UUID, payload any) error {
	if m.Events == nil {
		return nil
	}
	_, err := m.Events.Publish(ctx, tx, event, aggregate, &aid, &property, payload)
	return err
}

// live pushes a change to every open tournament screen when tx commits
// (leaderboards refresh within seconds, FR-TRN-08 ≤ 1 minute).
func live(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, kind string, extra map[string]any) error {
	data := map[string]any{"tournamentId": tid.String()}
	for k, v := range extra {
		data[k] = v
	}
	return realtime.Publish(ctx, tx, Topic, kind, &property, data)
}

// notifyCustomer sends a tournament notification to the player (portal
// user, else e-mail).
func (m *Module) notifyCustomer(ctx context.Context, tx pgx.Tx, property uuid.UUID, email, name string, user *uuid.UUID, event string, data map[string]any) error {
	if m.Notify == nil {
		return nil
	}
	msg := notify.Message{Event: event, Category: "golf", PropertyID: &property, Data: data}
	switch {
	case user != nil:
		msg.UserIDs = []uuid.UUID{*user}
	case strings.Contains(email, "@"):
		msg.Email, msg.Name, msg.Channels = email, name, []string{notify.ChannelEmail}
	default:
		return nil
	}
	return m.Notify.Send(ctx, tx, msg)
}

func jsonOf(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func itoa(n int) string { return fmt.Sprint(n) }

func now() time.Time { return clock.Now() }

// ── catalogue ─────────────────────────────────────────────────────────────

// Permissions of Golf → Tournaments (Naming Convention §6).
var permissionCodes = []struct{ object, action, desc string }{
	{"tournament", "view", "View tournaments, start sheets and results"},
	{"tournament", "manage", "Set up tournaments: schedule, divisions, packages, fees, registration window, cancel"},
	{"tournament", "start", "Shotgun start / tee-off of tournament flights (Starter)"},
	{"tournament", "finalize", "Finalize results: awards, Hall of Fame, results published"},
	{"tournament_registration", "view", "View tournament participants"},
	{"tournament_registration", "manage", "Register, change and withdraw participants; take tournament fees"},
	{"tournament_registration", "check_in", "Check in tournament participants (Tournament Desk)"},
	{"tournament_registration", "waive_fee", "Request a tournament fee waiver (approval)"},
	{"tournament_draw", "manage", "Flighting and tee assignment (draw, shotgun groups, caddies)"},
	{"tournament_score", "view", "View tournament scores (scoring desk)"},
	{"tournament_score", "enter", "Enter tournament scores (scoring desk, caddy tablet)"},
	{"tournament_score", "validate", "Attest and validate tournament scorecards; DQ / WD / NR"},
	{"tournament_score", "correct", "Correct a validated tournament scorecard (with reason)"},
	{"tournament_sponsor", "view", "View tournament sponsors"},
	{"tournament_sponsor", "manage", "Manage tournament sponsors"},
	{"tournament_sponsor", "invoice", "Invoice a sponsorship to the sponsor's account"},
	{"tournament_prize", "view", "View tournament prizes"},
	{"tournament_prize", "manage", "Manage tournament prizes"},
	{"tournament_prize", "award", "Award special prizes (Nearest to Pin, Longest Drive) and record hand-over"},
	{"tournament_leaderboard", "view", "Live leaderboard and the Leaderboard Screen"},
}

// Contribution adds the tournament permissions and grants them to the role
// templates (Product Overview §44; PRD P3 §4 personas).
func Contribution() catalog.Contribution {
	var perms []catalog.Permission
	all := []string{}
	for _, p := range permissionCodes {
		code := "golf." + p.object + "." + p.action
		perms = append(perms, catalog.Permission{Code: code, Description: p.desc})
		all = append(all, code)
	}
	except := func(skip ...string) []string {
		out := []string{}
		for _, c := range all {
			keep := true
			for _, s := range skip {
				if c == s {
					keep = false
				}
			}
			if keep {
				out = append(out, c)
			}
		}
		return out
	}
	view := []string{"golf.tournament.view", "golf.tournament_registration.view", "golf.tournament_score.view", "golf.tournament_sponsor.view",
		"golf.tournament_prize.view", "golf.tournament_leaderboard.view"}
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin": all,
			"golf_manager":   all,
			"golf_admin":     except("golf.tournament.finalize", "golf.tournament_sponsor.invoice"),
			"starter_marshal": {"golf.tournament.view", "golf.tournament.start", "golf.tournament_registration.view", "golf.tournament_registration.check_in",
				"golf.tournament_score.view", "golf.tournament_score.enter", "golf.tournament_score.validate", "golf.tournament_leaderboard.view",
				"golf.tournament_prize.view", "golf.tournament_prize.award"},
			"caddy":             {"golf.tournament_score.enter"},
			"caddy_manager":     {"golf.tournament.view", "golf.tournament_draw.manage"},
			"front_desk":        {"golf.tournament.view", "golf.tournament_registration.view", "golf.tournament_registration.manage", "golf.tournament_registration.check_in", "golf.tournament_leaderboard.view"},
			"reservation_staff": {"golf.tournament.view", "golf.tournament_registration.view", "golf.tournament_registration.manage"},
			"general_manager":   view,
			"club_manager":      view,
			"marketing_staff": {"golf.tournament.view", "golf.tournament_registration.view", "golf.tournament_sponsor.view", "golf.tournament_sponsor.manage",
				"golf.tournament_prize.view", "golf.tournament_leaderboard.view"},
			"sales_executive": {"golf.tournament.view", "golf.tournament_sponsor.view"},
			"finance_manager": {"golf.tournament.view", "golf.tournament_registration.view", "golf.tournament_sponsor.view", "golf.tournament_sponsor.invoice",
				"golf.tournament_prize.view"},
			"accountant": {"golf.tournament.view", "golf.tournament_sponsor.view", "golf.tournament_sponsor.invoice"},
			// The clubhouse TV (role Screen) opens the Leaderboard Screen only.
			"screen": {"golf.tournament_leaderboard.view"},
		},
	}
}

// Templates are the notification templates of tournaments (PRD P3
// FR-INT-P3-06: registration, waitlist, draw, final leaderboard; ID/EN).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"golf.tournament_registered": {
			"en": {"Registered: {{.tournament}}", "Hello {{.name}},\n\nYou are registered for {{.tournament}} on {{.date}} (registration {{.number}}). Tournament fee: {{.fee}} ({{.payment}})."},
			"id": {"Terdaftar: {{.tournament}}", "Halo {{.name}},\n\nAnda terdaftar di {{.tournament}} pada {{.date}} (registrasi {{.number}}). Biaya tournament: {{.fee}} ({{.payment}})."},
		},
		"golf.tournament_waitlisted": {
			"en": {"Waitlist: {{.tournament}}", "Hello {{.name}},\n\n{{.tournament}} is full; you are number {{.position}} on the waitlist (registration {{.number}}). We will let you know when a place becomes free."},
			"id": {"Daftar tunggu: {{.tournament}}", "Halo {{.name}},\n\n{{.tournament}} sudah penuh; Anda nomor {{.position}} di daftar tunggu (registrasi {{.number}}). Kami kabari bila ada tempat."},
		},
		"golf.tournament_promoted": {
			"en": {"A place is free: {{.tournament}}", "Hello {{.name}},\n\nA place became free and you are now registered for {{.tournament}} on {{.date}} (registration {{.number}}). Tournament fee: {{.fee}}, please pay by {{.due}}."},
			"id": {"Ada tempat: {{.tournament}}", "Halo {{.name}},\n\nAda tempat kosong dan Anda kini terdaftar di {{.tournament}} pada {{.date}} (registrasi {{.number}}). Biaya tournament: {{.fee}}, mohon dibayar sebelum {{.due}}."},
		},
		"golf.tournament_withdrawn": {
			"en": {"Withdrawn: {{.tournament}}", "Hello {{.name}},\n\nYour registration {{.number}} for {{.tournament}} is withdrawn ({{.reason}}). Refund: {{.refund}}."},
			"id": {"Pengunduran diri: {{.tournament}}", "Halo {{.name}},\n\nRegistrasi {{.number}} untuk {{.tournament}} dibatalkan ({{.reason}}). Refund: {{.refund}}."},
		},
		"golf.tournament_draw_published": {
			"en": {"Start sheet: {{.tournament}}", "Hello {{.name}},\n\nRound {{.round}} of {{.tournament}}: flight {{.flight}}, start {{.start}} at {{.time}}."},
			"id": {"Start sheet: {{.tournament}}", "Halo {{.name}},\n\nRonde {{.round}} {{.tournament}}: flight {{.flight}}, mulai {{.start}} pukul {{.time}}."},
		},
		"golf.tournament_results_published": {
			"en": {"Results: {{.tournament}}", "Hello {{.name}},\n\nThe results of {{.tournament}} are final. Your position: {{.position}} ({{.category}}, {{.score}})."},
			"id": {"Hasil: {{.tournament}}", "Halo {{.name}},\n\nHasil {{.tournament}} sudah final. Posisi Anda: {{.position}} ({{.category}}, {{.score}})."},
		},
		"golf.tournament_from_quotation": {
			"en": {"New corporate tournament: {{.tournament}}", "Quotation {{.quotation}} was accepted: the draft tournament {{.tournament}} on {{.date}} ({{.pax}} players) is ready to set up."},
			"id": {"Tournament korporat baru: {{.tournament}}", "Quotation {{.quotation}} diterima: draft tournament {{.tournament}} pada {{.date}} ({{.pax}} pemain) siap disiapkan."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}
