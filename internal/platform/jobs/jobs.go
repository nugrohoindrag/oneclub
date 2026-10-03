// Package jobs wraps River, the PostgreSQL-backed job queue (FR-JOB-01).
// Jobs are inserted inside business transactions, so a job exists if and
// only if the change that caused it committed.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"oneclub/internal/platform/provision"
)

// Queues.
const (
	QueueDefault       = river.QueueDefault
	QueueNotifications = "notifications"
	QueueIntegrations  = "integrations"
	QueueOutbox        = "outbox"
	QueueMaintenance   = "maintenance"
)

// FailureHook is told about every job failure; final=true when the job was
// discarded after exhausting its attempts (FR-JOB-05 alerting).
type FailureHook func(ctx context.Context, job *rivertype.JobRow, err error, final bool)

// Client is the shared River client.
type Client struct {
	River   *river.Client[pgx.Tx]
	Workers *river.Workers
}

// Registrar collects workers and periodic jobs from modules.
type Registrar struct {
	Workers  *river.Workers
	Periodic []*river.PeriodicJob
}

func NewRegistrar() *Registrar { return &Registrar{Workers: river.NewWorkers()} }

// Options configure the client.
type Options struct {
	Process     bool // true for `oneclub worker`; false = insert-only (API)
	Concurrency int
	OnFailure   FailureHook
	Logger      *slog.Logger
	TestOnly    bool
}

type errorHandler struct{ hook FailureHook }

func (h errorHandler) HandleError(ctx context.Context, job *rivertype.JobRow, err error) *river.ErrorHandlerResult {
	if h.hook != nil {
		h.hook(ctx, job, err, job.Attempt >= job.MaxAttempts)
	}
	return nil
}

func (h errorHandler) HandlePanic(ctx context.Context, job *rivertype.JobRow, panicVal any, trace string) *river.ErrorHandlerResult {
	slog.ErrorContext(ctx, "job panic", "kind", job.Kind, "job_id", job.ID, "panic", panicVal)
	if h.hook != nil {
		h.hook(ctx, job, fmt.Errorf("panic: %v", panicVal), job.Attempt >= job.MaxAttempts)
	}
	return nil
}

// New builds a River client on pool.
func New(pool *pgxpool.Pool, reg *Registrar, o Options) (*Client, error) {
	cfg := &river.Config{
		Schema:       provision.RiverSchema,
		Workers:      reg.Workers,
		ErrorHandler: errorHandler{hook: o.OnFailure},
		Logger:       o.Logger,
		MaxAttempts:  10,
		JobTimeout:   2 * time.Minute,
		TestOnly:     o.TestOnly,
	}
	if o.Process {
		c := o.Concurrency
		if c <= 0 {
			c = 20
		}
		cfg.Queues = map[string]river.QueueConfig{
			QueueDefault:       {MaxWorkers: c},
			QueueNotifications: {MaxWorkers: c},
			QueueIntegrations:  {MaxWorkers: c},
			QueueOutbox:        {MaxWorkers: 2},
			QueueMaintenance:   {MaxWorkers: 2},
		}
		cfg.PeriodicJobs = reg.Periodic
		cfg.FetchPollInterval = 500 * time.Millisecond
		cfg.FetchCooldown = 50 * time.Millisecond
	}
	rc, err := river.NewClient(riverpgxv5.New(pool), cfg)
	if err != nil {
		return nil, err
	}
	return &Client{River: rc, Workers: reg.Workers}, nil
}

// Insert enqueues a job in tx.
func (c *Client) Insert(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (int64, error) {
	res, err := c.River.InsertTx(ctx, tx, args, opts)
	if err != nil {
		return 0, err
	}
	return res.Job.ID, nil
}

// DailyAt is a periodic schedule that fires every day at hh:mm in a
// timezone — used for per-instance jobs such as end-of-day (FR-JOB-03).
type DailyAt struct {
	Hour, Minute int
	Location     func() *time.Location
}

// Next implements river.PeriodicSchedule.
func (d DailyAt) Next(now time.Time) time.Time {
	loc := time.UTC
	if d.Location != nil {
		if l := d.Location(); l != nil {
			loc = l
		}
	}
	t := now.In(loc)
	next := time.Date(t.Year(), t.Month(), t.Day(), d.Hour, d.Minute, 0, 0, loc)
	if !next.After(t) {
		next = next.AddDate(0, 0, 1)
	}
	return next.UTC()
}
