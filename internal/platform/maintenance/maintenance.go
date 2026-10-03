// Package maintenance holds platform periodic jobs: audit partitions,
// housekeeping, end-of-day (instance timezone, FR-JOB-03), approval SLA
// reminders (FR-APR-08) and the job health alert to Platform Admins
// (FR-JOB-05), delivered as in-app + e-mail notifications.
package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notification"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
)

// Deps are shared by maintenance workers.
type Deps struct {
	DB        *dbtx.DB
	Notify    notify.Sender
	Approvals *approval.Engine
	Cfg       *config.Config
	Location  func() *time.Location
}

func maint() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 5} }

// ── audit partitions ──────────────────────────────────────────────────────

type AuditPartitionsArgs struct{}

func (AuditPartitionsArgs) Kind() string                 { return "audit_partitions" }
func (AuditPartitionsArgs) InsertOpts() river.InsertOpts { return maint() }

type AuditPartitionsWorker struct {
	river.WorkerDefaults[AuditPartitionsArgs]
	D *Deps
}

func (w *AuditPartitionsWorker) Work(ctx context.Context, _ *river.Job[AuditPartitionsArgs]) error {
	var n int
	if err := w.D.DB.Primary.QueryRow(ctx, `SELECT audit.ensure_partitions(3)`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		slog.InfoContext(ctx, "audit partitions created", "count", n)
	}
	return nil
}

// ── housekeeping ──────────────────────────────────────────────────────────

type CleanupArgs struct{}

func (CleanupArgs) Kind() string                 { return "platform_cleanup" }
func (CleanupArgs) InsertOpts() river.InsertOpts { return maint() }

type CleanupWorker struct {
	river.WorkerDefaults[CleanupArgs]
	D *Deps
}

func (w *CleanupWorker) Work(ctx context.Context, _ *river.Job[CleanupArgs]) error {
	return w.D.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		for _, q := range []string{
			`DELETE FROM platform.idempotency_keys WHERE created_at < now() - interval '48 hours'`,
			`DELETE FROM platform.sessions WHERE (expires_at < now() - interval '30 days') OR (revoked_at < now() - interval '30 days')`,
			`DELETE FROM platform.password_reset_tokens WHERE expires_at < now() - interval '7 days'`,
			`DELETE FROM platform.outbox WHERE dispatched_at < now() - interval '30 days'`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
}

// ── end of day (FR-JOB-03) ────────────────────────────────────────────────

// EndOfDayArgs runs at 23:55 in the instance timezone. In P0 it records the
// business day close; P1 modules attach golf/billing end-of-day steps.
type EndOfDayArgs struct {
	BusinessDate string `json:"businessDate,omitempty"`
}

func (EndOfDayArgs) Kind() string                 { return "end_of_day" }
func (EndOfDayArgs) InsertOpts() river.InsertOpts { return maint() }

type EndOfDayWorker struct {
	river.WorkerDefaults[EndOfDayArgs]
	D *Deps
}

func (w *EndOfDayWorker) Work(ctx context.Context, job *river.Job[EndOfDayArgs]) error {
	loc := w.D.Location()
	date := job.Args.BusinessDate
	if date == "" {
		date = time.Now().In(loc).Format("2006-01-02")
	}
	sctx := authz.WithPrincipal(dbtx.System(ctx), authz.System())
	return w.D.DB.WithTx(sctx, func(tx pgx.Tx) error {
		return audit.Record(sctx, tx, audit.Entry{Module: "platform", Action: "end_of_day", Category: audit.CategorySystem,
			EntityType: "platform.business_day", EntityID: date, EntityLabel: "End of day " + date,
			Metadata: map[string]any{"timezone": loc.String()}})
	})
}

// ── approval reminders (FR-APR-08) ────────────────────────────────────────

type ApprovalRemindersArgs struct{}

func (ApprovalRemindersArgs) Kind() string                 { return "approval_reminders" }
func (ApprovalRemindersArgs) InsertOpts() river.InsertOpts { return maint() }

type ApprovalRemindersWorker struct {
	river.WorkerDefaults[ApprovalRemindersArgs]
	D *Deps
}

func (w *ApprovalRemindersWorker) Work(ctx context.Context, _ *river.Job[ApprovalRemindersArgs]) error {
	_, err := w.D.Approvals.SendReminders(ctx)
	return err
}

// ── job health alert (FR-JOB-05) ──────────────────────────────────────────

// Thresholds for the job health alert.
const (
	RepeatedFailureAttempts = 3
	BacklogThreshold        = 200
	BacklogAge              = 10 * time.Minute
	OutboxLagThreshold      = 5 * time.Minute
	HealthInterval          = 5 * time.Minute
)

type JobHealthArgs struct{}

func (JobHealthArgs) Kind() string                 { return "job_health" }
func (JobHealthArgs) InsertOpts() river.InsertOpts { return maint() }

type JobHealthWorker struct {
	river.WorkerDefaults[JobHealthArgs]
	D *Deps
}

// Health is the result of one check.
type Health struct {
	Failed    []string // "kind #id (attempt n): error"
	Backlog   int
	OutboxLag time.Duration
}

// Check inspects River and the outbox for the last interval.
func Check(ctx context.Context, db *dbtx.DB, window time.Duration) (Health, error) {
	var h Health
	err := db.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT kind, id, attempt, coalesce(errors[array_length(errors, 1)]->>'error', '') FROM river.river_job
			WHERE (state = 'discarded' AND finalized_at > now() - $1::interval)
			   OR (state = 'retryable' AND attempt = $2 AND attempted_at > now() - $1::interval)
			ORDER BY id DESC LIMIT 20`, fmt.Sprintf("%d seconds", int(window.Seconds())), RepeatedFailureAttempts)
		if err != nil {
			return err
		}
		for rows.Next() {
			var kind, msg string
			var jid int64
			var attempt int
			if err := rows.Scan(&kind, &jid, &attempt, &msg); err != nil {
				rows.Close()
				return err
			}
			if len(msg) > 160 {
				msg = msg[:160] + "…"
			}
			h.Failed = append(h.Failed, fmt.Sprintf("%s #%d (attempt %d): %s", kind, jid, attempt, msg))
		}
		rows.Close()
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM river.river_job WHERE state = 'available' AND scheduled_at < now() - $1::interval`,
			fmt.Sprintf("%d seconds", int(BacklogAge.Seconds()))).Scan(&h.Backlog); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return h, err
	}
	_, h.OutboxLag, err = outbox.Stats(ctx, db)
	return h, err
}

// Alert notifies Platform Admins when the health check finds a problem.
func Alert(ctx context.Context, d *Deps, h Health) (bool, error) {
	var parts []string
	if len(h.Failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d job(s) failing repeatedly", len(h.Failed)))
	}
	if h.Backlog > BacklogThreshold {
		parts = append(parts, fmt.Sprintf("%d jobs waiting longer than %s", h.Backlog, BacklogAge))
	}
	if h.OutboxLag > OutboxLagThreshold {
		parts = append(parts, fmt.Sprintf("outbox lag %s", h.OutboxLag.Round(time.Second)))
	}
	if len(parts) == 0 {
		return false, nil
	}
	sctx := dbtx.System(ctx)
	err := d.DB.WithTx(sctx, func(tx pgx.Tx) error {
		admins, err := notification.UserIDsWithPermission(sctx, tx, catalog.ShellPlatformAdmin, nil)
		if err != nil || len(admins) == 0 {
			return err
		}
		details := strings.Join(h.Failed, "\n")
		if details == "" {
			details = strings.Join(parts, "; ")
		}
		return d.Notify.Send(sctx, tx, notify.Message{Event: "system.job_alert", Category: "system", UserIDs: admins, Mandatory: true,
			Link: d.Cfg.PublicBaseURL + "/settings/system/background-jobs",
			Data: map[string]any{"summary": strings.Join(parts, "; "), "details": details, "link": d.Cfg.PublicBaseURL + "/settings/system/background-jobs"}})
	})
	return err == nil, err
}

func (w *JobHealthWorker) Work(ctx context.Context, _ *river.Job[JobHealthArgs]) error {
	h, err := Check(ctx, w.D.DB, HealthInterval)
	if err != nil {
		return err
	}
	_, err = Alert(ctx, w.D, h)
	return err
}

// Register adds the workers and schedules.
func Register(reg *jobs.Registrar, d *Deps) {
	river.AddWorker(reg.Workers, &AuditPartitionsWorker{D: d})
	river.AddWorker(reg.Workers, &CleanupWorker{D: d})
	river.AddWorker(reg.Workers, &EndOfDayWorker{D: d})
	river.AddWorker(reg.Workers, &ApprovalRemindersWorker{D: d})
	river.AddWorker(reg.Workers, &JobHealthWorker{D: d})
	every := func(dur time.Duration, args river.JobArgs, start bool) *river.PeriodicJob {
		return river.NewPeriodicJob(river.PeriodicInterval(dur), func() (river.JobArgs, *river.InsertOpts) { return args, nil },
			&river.PeriodicJobOpts{RunOnStart: start})
	}
	daily := func(h, m int, args river.JobArgs) *river.PeriodicJob {
		return river.NewPeriodicJob(jobs.DailyAt{Hour: h, Minute: m, Location: d.Location},
			func() (river.JobArgs, *river.InsertOpts) { return args, nil }, nil)
	}
	reg.Periodic = append(reg.Periodic,
		every(24*time.Hour, AuditPartitionsArgs{}, true),
		every(15*time.Minute, ApprovalRemindersArgs{}, false),
		every(HealthInterval, JobHealthArgs{}, false),
		daily(2, 0, CleanupArgs{}),
		daily(23, 55, EndOfDayArgs{}),
	)
}
