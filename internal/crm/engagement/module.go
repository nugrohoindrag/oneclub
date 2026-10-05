package engagement

// Catalogue (permissions and role templates), notification templates
// (FR-INT-P3-06), approval document types and background jobs of Customer
// Engagement: the campaign dispatcher (throttled batches with retry through
// the notification queue, FR-CMP-07), the daily reminders (FR-CMP-06), the
// complaint SLA timer (FR-TKT-02/03) and the daily refresh of dynamic
// segments (FR-C360-03).

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/crm"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Permissions of engagement (P2's segment, campaign and feedback
// permissions stay in the crm root contribution).
func Permissions() []catalog.Permission {
	perms := resource.Permissions(ReminderRules, Suppressions, TicketCategories)
	perms = append(perms, catalog.P("crm", "ticket", "view", "create", "manage", "escalate", "compensate")...)
	return perms
}

// RolePermissions maps the engagement permissions to the role templates.
func RolePermissions() map[string][]string {
	tickets := []string{"crm.ticket.view", "crm.ticket.create", "crm.ticket.manage", "crm.ticket.escalate", "crm.ticket_category.view"}
	all := append(resource.AllActions(ReminderRules, Suppressions, TicketCategories), "crm.ticket.view", "crm.ticket.create", "crm.ticket.manage",
		"crm.ticket.escalate", "crm.ticket.compensate", "crm.segment.update", "crm.campaign.send")
	manager := append(append([]string{}, tickets...), "crm.ticket.compensate")
	desk := []string{"crm.ticket.view", "crm.ticket.create", "crm.ticket_category.view"}
	return map[string][]string{
		"property_admin":          all,
		"crm_admin":               all,
		"marketing_staff":         append(resource.AllActions(ReminderRules, Suppressions), "crm.ticket.view", "crm.ticket_category.view"),
		"general_manager":         manager,
		"club_manager":            manager,
		"golf_manager":            manager,
		"sport_club_manager":      manager,
		"resort_manager":          manager,
		"outlet_manager":          manager,
		"banquet_manager":         manager,
		"membership_manager":      manager,
		"front_desk":              desk,
		"sport_club_receptionist": desk,
		"reservation_staff":       desk,
		"membership_admin":        desk,
		"golf_admin":              desk,
		"cashier":                 desk,
		"pos_staff":               desk,
	}
}

// DocumentTypes are the approval document types of engagement.
func DocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{CampaignSendDocumentType, CompensationDocumentType}
}

// Templates are the notification templates of campaigns, reminders and
// complaint tickets in Indonesian and English (FR-INT-P3-06).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		CampaignTemplate: {
			"en": {"{{.subject}}", "{{.message}}\n\nStop receiving these messages: {{.unsubscribeLink}}"},
			"id": {"{{.subject}}", "{{.message}}\n\nBerhenti menerima pesan ini: {{.unsubscribeLink}}"},
		},
		ReminderTemplate: {
			"en": {"{{.subject}}", "{{.message}}\n\nCommunication preferences: {{.link}}"},
			"id": {"{{.subject}}", "{{.message}}\n\nPreferensi komunikasi: {{.link}}"},
		},
		"crm.birthday_greeting": {
			"en": {"Happy birthday, {{.name}}!", "Happy birthday, {{.name}}! Celebrate with us{{if .promoCode}} — use code {{.promoCode}} for your birthday treat{{end}}."},
			"id": {"Selamat ulang tahun, {{.name}}!", "Selamat ulang tahun, {{.name}}! Rayakan bersama kami{{if .promoCode}} — gunakan kode {{.promoCode}} untuk hadiah ulang tahun Anda{{end}}."},
		},
		"crm.renewal_offer": {
			"en": {"Your membership ends on {{.date}}", "Hello {{.name}}, your {{.detail}} membership ends on {{.date}}. Renew now{{if .promoCode}} with code {{.promoCode}}{{end}}."},
			"id": {"Keanggotaan Anda berakhir {{.date}}", "Halo {{.name}}, keanggotaan {{.detail}} Anda berakhir pada {{.date}}. Perpanjang sekarang{{if .promoCode}} dengan kode {{.promoCode}}{{end}}."},
		},
		"crm.follow_up_message": {
			"en": {"We miss you, {{.name}}", "Hello {{.name}}, it has been a while since your last visit ({{.date}}). We would love to see you again{{if .promoCode}} — code {{.promoCode}}{{end}}."},
			"id": {"Kami merindukan Anda, {{.name}}", "Halo {{.name}}, sudah lama sejak kunjungan terakhir Anda ({{.date}}). Kami tunggu kedatangan Anda{{if .promoCode}} — kode {{.promoCode}}{{end}}."},
		},
		"crm.ticket_created": {
			"en": {"Complaint {{.number}} received", "Hello {{.name}}, we received your complaint \"{{.subject}}\" ({{.number}}). {{.message}}"},
			"id": {"Keluhan {{.number}} diterima", "Halo {{.name}}, keluhan Anda \"{{.subject}}\" ({{.number}}) sudah kami terima. {{.message}}"},
		},
		"crm.ticket_update": {
			"en": {"Update on complaint {{.number}}", "Hello {{.name}}, complaint {{.number}} ({{.subject}}) is now {{.status}}: {{.message}}"},
			"id": {"Pembaruan keluhan {{.number}}", "Halo {{.name}}, keluhan {{.number}} ({{.subject}}) kini berstatus {{.status}}: {{.message}}"},
		},
		"crm.ticket_assigned": {
			"en": {"Complaint {{.number}} assigned to you", "{{.priority}} priority complaint {{.number}} ({{.businessLine}}): {{.subject}}. {{.reason}}"},
			"id": {"Keluhan {{.number}} ditugaskan kepada Anda", "Keluhan prioritas {{.priority}} {{.number}} ({{.businessLine}}): {{.subject}}. {{.reason}}"},
		},
		"crm.ticket_escalated": {
			"en": {"Complaint {{.number}} escalated (level {{.level}})", "{{.priority}} priority complaint {{.number}} of {{.customer}} ({{.businessLine}}) was escalated: {{.reason}}. Subject: {{.subject}}"},
			"id": {"Keluhan {{.number}} dieskalasi (level {{.level}})", "Keluhan prioritas {{.priority}} {{.number}} dari {{.customer}} ({{.businessLine}}) dieskalasi: {{.reason}}. Perihal: {{.subject}}"},
		},
		"crm.ticket_customer_reply": {
			"en": {"Customer replied on complaint {{.number}}", "The customer replied on complaint {{.number}} ({{.subject}}): {{.reason}}"},
			"id": {"Pelanggan membalas keluhan {{.number}}", "Pelanggan membalas keluhan {{.number}} ({{.subject}}): {{.reason}}"},
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

// ── P2 campaign send ──────────────────────────────────────────────────────

// SendNow is P2's "send campaign" (crm root hook): the campaign is queued
// now through the Campaign Policies (approval above the threshold) and its
// first batch is sent in the request — per-channel consent, suppression
// and frequency cap apply; the dispatcher sends the rest.
func (s *Service) SendNow(ctx context.Context, tx pgx.Tx, property, cid uuid.UUID) (crm.CampaignResult, error) {
	c, err := getCampaign(ctx, tx, property, cid, true)
	if err != nil {
		return crm.CampaignResult{}, err
	}
	if c.Status != "draft" {
		return crm.CampaignResult{}, conflictSent(c)
	}
	after, err := s.Schedule(ctx, tx, property, cid, CampaignScheduleInput{})
	if err != nil {
		return crm.CampaignResult{}, err
	}
	res := crm.CampaignResult{CampaignID: cid}
	if after.Status != "scheduled" {
		return res, nil // waits for approval (FR-CMP-08)
	}
	if _, _, err := s.dispatchOne(ctx, tx, property, cid); err != nil {
		return res, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'sent')::int, count(*) FILTER (WHERE status LIKE 'skipped%')::int
		FROM crm.campaign_recipients WHERE campaign_id = $1`, cid).Scan(&res.Sent, &res.Skipped)
	return res, err
}

// ── jobs ──────────────────────────────────────────────────────────────────

// DispatchArgs sends the due campaign batches.
type DispatchArgs struct{}

func (DispatchArgs) Kind() string { return "crm_campaign_dispatch" }

func (DispatchArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueNotifications, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByPeriod: 30 * time.Second}}
}

type dispatchWorker struct {
	river.WorkerDefaults[DispatchArgs]
	S *Service
}

func (w *dispatchWorker) Work(ctx context.Context, _ *river.Job[DispatchArgs]) error {
	_, err := w.S.DispatchDue(ctx)
	return err
}

// RemindersArgs sends today's birthday, renewal and follow-up reminders.
type RemindersArgs struct{}

func (RemindersArgs) Kind() string { return "crm_reminders" }

func (RemindersArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueNotifications, MaxAttempts: 3}
}

type remindersWorker struct {
	river.WorkerDefaults[RemindersArgs]
	S *Service
}

func (w *remindersWorker) Work(ctx context.Context, _ *river.Job[RemindersArgs]) error {
	_, err := w.S.RunReminders(ctx)
	return err
}

// TicketSLAArgs runs the complaint SLA timer.
type TicketSLAArgs struct{}

func (TicketSLAArgs) Kind() string { return "crm_ticket_sla" }

func (TicketSLAArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
}

type slaWorker struct {
	river.WorkerDefaults[TicketSLAArgs]
	S *Service
}

func (w *slaWorker) Work(ctx context.Context, _ *river.Job[TicketSLAArgs]) error {
	_, err := w.S.RunSLA(ctx)
	return err
}

// SegmentRefreshArgs refreshes the dynamic segments.
type SegmentRefreshArgs struct{}

func (SegmentRefreshArgs) Kind() string { return "crm_segment_refresh" }

func (SegmentRefreshArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type segmentWorker struct {
	river.WorkerDefaults[SegmentRefreshArgs]
	S *Service
}

func (w *segmentWorker) Work(ctx context.Context, _ *river.Job[SegmentRefreshArgs]) error {
	_, err := w.S.RefreshDynamic(ctx)
	return err
}

// RegisterJobs adds the engagement workers and schedules: campaign batches
// every minute, the SLA timer every 5 minutes, dynamic segments at 01:30
// and reminders at 08:00 (club time).
func (s *Service) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &dispatchWorker{S: s})
	river.AddWorker(reg.Workers, &remindersWorker{S: s})
	river.AddWorker(reg.Workers, &slaWorker{S: s})
	river.AddWorker(reg.Workers, &segmentWorker{S: s})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) { return DispatchArgs{}, nil }, nil),
		river.NewPeriodicJob(river.PeriodicInterval(5*time.Minute), func() (river.JobArgs, *river.InsertOpts) { return TicketSLAArgs{}, nil }, nil),
		river.NewPeriodicJob(jobs.DailyAt{Hour: 1, Minute: 30, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return SegmentRefreshArgs{}, nil }, nil),
		river.NewPeriodicJob(jobs.DailyAt{Hour: 8, Minute: 0, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return RemindersArgs{}, nil }, nil))
}
