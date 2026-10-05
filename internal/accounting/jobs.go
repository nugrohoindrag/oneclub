package accounting

// Daily job of Accounting: the recurring journals due (FR-ACC-08) of every
// property with a book.

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/jobs"
)

// DailyArgs runs the daily accounting job.
type DailyArgs struct{}

func (DailyArgs) Kind() string { return "accounting_daily" }
func (DailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// DailyWorker posts the recurring journals due.
type DailyWorker struct {
	river.WorkerDefaults[DailyArgs]
	M *Module
}

func (w *DailyWorker) Work(ctx context.Context, _ *river.Job[DailyArgs]) error {
	_, err := w.M.RunDaily(ctx)
	return err
}

// RegisterJobs adds the accounting worker and its daily schedule.
func (m *Module) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &DailyWorker{M: m})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(jobs.DailyAt{Hour: 0, Minute: 40, Location: loc},
		func() (river.JobArgs, *river.InsertOpts) { return DailyArgs{}, nil }, nil))
}

// RunDaily posts the recurring journals due today of every book.
func (m *Module) RunDaily(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	var props []uuid.UUID
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT property_id FROM accounting.books ORDER BY property_id`)
		if err != nil {
			return err
		}
		props, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, p := range props {
		err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			res, err := m.RunRecurring(ctx, tx, p, localToday(ctx, tx, p))
			n += res.Posted
			for _, e := range res.Errors {
				slog.WarnContext(ctx, "recurring journal not posted", "property", p, "err", e)
			}
			return err
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
