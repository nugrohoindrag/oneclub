package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/jobs"
)

// DispatchArgs triggers an outbox dispatch run.
type DispatchArgs struct{}

func (DispatchArgs) Kind() string { return "outbox_dispatch" }

func (DispatchArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueOutbox, MaxAttempts: 25}
}

// Dispatcher is the River worker that delivers outbox events.
type Dispatcher struct {
	river.WorkerDefaults[DispatchArgs]
	DB    *dbtx.DB
	Bus   *Bus
	Batch int
}

// Work dispatches pending events in batches until none remain.
func (d *Dispatcher) Work(ctx context.Context, _ *river.Job[DispatchArgs]) error {
	_, err := d.DispatchPending(ctx)
	return err
}

// DispatchPending processes all currently pending events; returns how many
// were fully dispatched. A failing subscriber leaves its event pending for
// the next run (and the job fails, so it is retried with backoff).
func (d *Dispatcher) DispatchPending(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	batch := d.Batch
	if batch <= 0 {
		batch = 100
	}
	done := 0
	var failures []string
	for {
		n, fails, err := d.dispatchBatch(ctx, batch)
		if err != nil {
			return done, err
		}
		done += n
		failures = append(failures, fails...)
		if n+len(fails) < batch || n == 0 {
			break
		}
	}
	if len(failures) > 0 {
		return done, fmt.Errorf("outbox: %d subscriber failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return done, nil
}

func (d *Dispatcher) dispatchBatch(ctx context.Context, batch int) (int, []string, error) {
	var ids []string
	err := d.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM platform.outbox WHERE dispatched_at IS NULL ORDER BY occurred_at LIMIT $1`, batch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			ids = append(ids, s)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, nil, err
	}
	dispatched := 0
	var failures []string
	for _, eid := range ids {
		ok, ferr := d.dispatchOne(ctx, eid)
		if ferr != nil {
			failures = append(failures, ferr.Error())
		}
		if ok {
			dispatched++
		}
	}
	return dispatched, failures, nil
}

// dispatchOne locks one event (SKIP LOCKED, so concurrent dispatchers never
// double-process) and runs every subscriber that has not processed it yet.
func (d *Dispatcher) dispatchOne(ctx context.Context, eid string) (bool, error) {
	var dispatched bool
	var subErr error
	err := d.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var e Event
		err := tx.QueryRow(ctx, `
			SELECT id, event_type, aggregate_type, aggregate_id, property_id, payload, occurred_at
			FROM platform.outbox WHERE id = $1 AND dispatched_at IS NULL FOR UPDATE SKIP LOCKED`, eid).
			Scan(&e.ID, &e.Type, &e.AggregateType, &e.AggregateID, &e.PropertyID, &e.Payload, &e.OccurredAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var failed []string
		for _, s := range d.Bus.subscribers(e.Type) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.outbox_processed WHERE subscriber = $1 AND event_id = $2)`,
				s.name, e.ID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			sp, err := tx.Begin(ctx) // savepoint
			if err != nil {
				return err
			}
			if herr := s.fn(ctx, sp, e); herr != nil {
				_ = sp.Rollback(ctx)
				failed = append(failed, s.name+": "+herr.Error())
				slog.WarnContext(ctx, "outbox subscriber failed", "event", e.Type, "event_id", e.ID, "subscriber", s.name, "err", herr)
				continue
			}
			if _, err := sp.Exec(ctx, `INSERT INTO platform.outbox_processed (subscriber, event_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, s.name, e.ID); err != nil {
				_ = sp.Rollback(ctx)
				return err
			}
			if err := sp.Commit(ctx); err != nil {
				return err
			}
		}
		if len(failed) > 0 {
			subErr = fmt.Errorf("event %s %s: %s", e.Type, e.ID, strings.Join(failed, ", "))
			_, err := tx.Exec(ctx, `UPDATE platform.outbox SET attempts = attempts + 1, last_error = $2 WHERE id = $1`, e.ID, strings.Join(failed, "; "))
			return err
		}
		dispatched = true
		_, err = tx.Exec(ctx, `UPDATE platform.outbox SET dispatched_at = now(), attempts = attempts + 1, last_error = NULL WHERE id = $1`, e.ID)
		return err
	})
	if err != nil {
		return false, err
	}
	return dispatched, subErr
}

// PeriodicDispatch is the safety-net schedule: pending events are picked up
// even if a dispatch job was lost (e.g. worker restarted mid-process).
func PeriodicDispatch() *river.PeriodicJob {
	return river.NewPeriodicJob(river.PeriodicInterval(15*time.Second),
		func() (river.JobArgs, *river.InsertOpts) { return DispatchArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true})
}

// Stats reports pending count and the age of the oldest pending event.
func Stats(ctx context.Context, db *dbtx.DB) (pending int, oldest time.Duration, err error) {
	var age *float64
	err = db.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*), extract(epoch FROM now() - min(occurred_at)) FROM platform.outbox WHERE dispatched_at IS NULL`).
			Scan(&pending, &age)
	})
	if age != nil {
		oldest = time.Duration(*age * float64(time.Second))
	}
	return
}
