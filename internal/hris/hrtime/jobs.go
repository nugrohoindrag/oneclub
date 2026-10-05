package hrtime

// Daily time & attendance job (00:20 instance time, per property in its
// timezone): close the attendance days of yesterday and the day before
// (night shifts) — Absent for shifts without clock-in, Missing Clock-out,
// On Leave — and run the leave accrual, the carry-over expiry and the
// collective leave deductions.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/jobs"
)

// DailyArgs runs the daily time & attendance job.
type DailyArgs struct{}

func (DailyArgs) Kind() string { return "hris_time_daily" }
func (DailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// DailyWorker runs RunDaily.
type DailyWorker struct {
	river.WorkerDefaults[DailyArgs]
	M *Module
}

func (w *DailyWorker) Work(ctx context.Context, _ *river.Job[DailyArgs]) error {
	_, err := w.M.RunDaily(ctx)
	return err
}

// RegisterJobs adds the worker and its daily schedule.
func (m *Module) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &DailyWorker{M: m})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(jobs.DailyAt{Hour: 0, Minute: 20, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return DailyArgs{}, nil }, nil))
}

// DailyReport counts what the daily job did.
type DailyReport struct {
	Properties int                `json:"properties"`
	Accrual    LeaveAccrualReport `json:"accrual"`
}

// RunDaily runs the daily job for every active property.
func (m *Module) RunDaily(ctx context.Context) (DailyReport, error) {
	ctx = dbtx.System(ctx)
	var rep DailyReport
	var props []uuid.UUID
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active' ORDER BY created_at`)
		if err != nil {
			return err
		}
		props, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	}); err != nil {
		return rep, err
	}
	for _, p := range props {
		pctx := reqctx.WithProperty(ctx, p)
		if err := m.DB.WithTx(pctx, func(tx pgx.Tx) error {
			tday := today(pctx, tx, p)
			if err := m.FinalizePeriod(pctx, tx, p, tday.AddDate(0, 0, -2), tday.AddDate(0, 0, -1)); err != nil {
				return err
			}
			a, err := m.accrue(pctx, tx, p, nil)
			rep.Accrual.Granted += a.Granted
			rep.Accrual.CarriedOver += a.CarriedOver
			rep.Accrual.Expired += a.Expired
			rep.Accrual.Collective += a.Collective
			return err
		}); err != nil {
			return rep, fmt.Errorf("hris time daily for %s: %w", p, err)
		}
		rep.Properties++
	}
	return rep, nil
}
