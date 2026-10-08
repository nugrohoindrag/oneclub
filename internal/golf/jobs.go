package golf

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/reservation"
)

func chiParam(r *http.Request, name string) string { return chi.URLParam(r, name) }

// ── expiry of holds and unpaid bookings (FR-BKG-01, FR-PAY-02 AC) ─────────

// ExpireResult reports one run.
type ExpireResult struct {
	Holds  int `json:"holds"`
	Unpaid int `json:"unpaid"`
}

// ExpireHolds releases lapsed tee holds and cancels online bookings not
// paid in time, returning their slots to Available on every channel.
func (m *Module) ExpireHolds(ctx context.Context) (ExpireResult, error) {
	var res ExpireResult
	ctx = dbtx.System(ctx)
	type item struct {
		id, property uuid.UUID
		status       string
	}
	var list []item
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, property_id, status FROM golf.bookings WHERE (status = 'draft' AND hold_expires_at <= now())
			OR (status = 'pending' AND payment_due_at <= now()) LIMIT 500`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x item
			if err := rows.Scan(&x.id, &x.property, &x.status); err != nil {
				return err
			}
			list = append(list, x)
		}
		return rows.Err()
	})
	if err != nil {
		return res, err
	}
	for _, x := range list {
		pctx := reqctx.WithProperty(ctx, x.property)
		err := m.DB.WithTx(pctx, func(tx pgx.Tx) error {
			var st string
			if err := tx.QueryRow(pctx, `SELECT status FROM golf.bookings WHERE id = $1 FOR UPDATE`, x.id).Scan(&st); err != nil {
				return err
			}
			if st != x.status {
				return nil
			}
			if st == "draft" {
				res.Holds++
				if err := m.ReleaseHold(pctx, tx, x.property, x.id, "hold expired"); err != nil {
					return err
				}
				return audit.Record(pctx, tx, audit.Entry{Module: "golf", Action: "hold_expired", Category: audit.CategorySystem, EntityType: "golf.booking",
					EntityID: x.id.String(), PropertyID: &x.property, ActorName: "golf job"})
			}
			res.Unpaid++
			_, err := m.Cancel(pctx, tx, x.property, x.id, CancelRequest{Reason: "payment not received in time (Payment Policy)"})
			return err
		})
		if err != nil {
			slog.WarnContext(ctx, "expire golf booking", "booking", x.id, "err", err)
		}
	}
	return res, nil
}

// AutoNoShow marks confirmed bookings without any check-in No-show once the
// grace period after the tee time has passed (FR-BKG-10).
func (m *Module) AutoNoShow(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	type item struct{ id, property uuid.UUID }
	var list []item
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT b.id, b.property_id FROM golf.bookings b WHERE b.status = 'confirmed' AND b.start_at < now() - interval '30 minutes'
			AND b.start_at > now() - interval '2 days'
			AND NOT EXISTS (SELECT 1 FROM golf.booking_players p WHERE p.booking_id = b.id AND p.status = 'checked_in') LIMIT 200`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x item
			if err := rows.Scan(&x.id, &x.property); err != nil {
				return err
			}
			list = append(list, x)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range list {
		pctx := reqctx.WithProperty(ctx, x.property)
		err := m.DB.WithTx(pctx, func(tx pgx.Tx) error {
			var start time.Time
			var created time.Time
			if err := tx.QueryRow(pctx, `SELECT start_at, created_at FROM golf.bookings WHERE id = $1 FOR UPDATE`, x.id).Scan(&start, &created); err != nil {
				return err
			}
			pol, err := LoadPoliciesAsOf(pctx, tx, x.property, created)
			if err != nil {
				return err
			}
			if clock.Now().Before(start.Add(time.Duration(pol.Golf.NoShowGraceMinutes) * time.Minute)) {
				return nil
			}
			_, err = m.NoShow(pctx, tx, x.property, x.id, "automatic: no check-in after the grace period")
			if err == nil {
				n++
			}
			return err
		})
		if err != nil {
			slog.WarnContext(ctx, "auto no-show", "booking", x.id, "err", err)
		}
	}
	return n, nil
}

// GenerateAll refreshes the tee sheets of every property.
func (m *Module) GenerateAll(ctx context.Context) error {
	ctx = dbtx.System(ctx)
	rows, err := m.DB.Primary.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active' AND archived_at IS NULL`)
	if err != nil {
		return err
	}
	var props []uuid.UUID
	for rows.Next() {
		var p uuid.UUID
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		props = append(props, p)
	}
	rows.Close()
	for _, p := range props {
		pctx := reqctx.WithProperty(ctx, p)
		err := m.DB.WithTx(pctx, func(tx pgx.Tx) error {
			from := localDay(clock.Now(), location(pctx, tx, p))
			_, err := m.Generate(pctx, tx, p, nil, from, m.maxWindow(pctx, tx, p))
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// SendReminders sends the H-1 reminder of tomorrow's bookings once.
func (m *Module) SendReminders(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	type item struct{ id, property uuid.UUID }
	var list []item
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT b.id, b.property_id FROM golf.bookings b WHERE b.status = 'confirmed'
			AND b.start_at BETWEEN now() + interval '12 hours' AND now() + interval '36 hours'
			AND NOT EXISTS (SELECT 1 FROM golf.booking_history h WHERE h.booking_id = b.id AND h.event = 'reminder_sent') LIMIT 1000`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x item
			if err := rows.Scan(&x.id, &x.property); err != nil {
				return err
			}
			list = append(list, x)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range list {
		pctx := reqctx.WithProperty(ctx, x.property)
		err := m.DB.WithTx(pctx, func(tx pgx.Tx) error {
			b, err := GetBooking(pctx, tx, x.id)
			if err != nil {
				return err
			}
			if err := m.notifyBooking(pctx, tx, x.property, b, "golf.booking_reminder", nil); err != nil {
				return err
			}
			n++
			return addHistory(pctx, tx, x.property, x.id, "reminder_sent", nil, nil, "")
		})
		if err != nil {
			slog.WarnContext(ctx, "booking reminder", "booking", x.id, "err", err)
		}
	}
	return n, nil
}

// ── River jobs ────────────────────────────────────────────────────────────

type HoldsArgs struct{}

func (HoldsArgs) Kind() string { return "golf_expire_holds" }
func (HoldsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type HoldsWorker struct {
	river.WorkerDefaults[HoldsArgs]
	M *Module
}

func (w *HoldsWorker) Work(ctx context.Context, _ *river.Job[HoldsArgs]) error {
	if _, err := reservation.ReleaseExpired(ctx, w.M.DB); err != nil {
		return err
	}
	_, err := w.M.ExpireHolds(ctx)
	return err
}

type NoShowArgs struct{}

func (NoShowArgs) Kind() string { return "golf_auto_no_show" }
func (NoShowArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type NoShowWorker struct {
	river.WorkerDefaults[NoShowArgs]
	M *Module
}

func (w *NoShowWorker) Work(ctx context.Context, _ *river.Job[NoShowArgs]) error {
	_, err := w.M.AutoNoShow(ctx)
	return err
}

type TeeSheetArgs struct{}

func (TeeSheetArgs) Kind() string { return "golf_generate_tee_sheets" }
func (TeeSheetArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type TeeSheetWorker struct {
	river.WorkerDefaults[TeeSheetArgs]
	M *Module
}

func (w *TeeSheetWorker) Work(ctx context.Context, _ *river.Job[TeeSheetArgs]) error {
	return w.M.GenerateAll(ctx)
}

type ReminderArgs struct{}

func (ReminderArgs) Kind() string { return "golf_booking_reminders" }
func (ReminderArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type ReminderWorker struct {
	river.WorkerDefaults[ReminderArgs]
	M *Module
}

func (w *ReminderWorker) Work(ctx context.Context, _ *river.Job[ReminderArgs]) error {
	_, err := w.M.SendReminders(ctx)
	return err
}

type LockerArgs struct{}

func (LockerArgs) Kind() string { return "golf_release_daily_lockers" }
func (LockerArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type LockerWorker struct {
	river.WorkerDefaults[LockerArgs]
	M *Module
}

func (w *LockerWorker) Work(ctx context.Context, _ *river.Job[LockerArgs]) error {
	_, err := w.M.ReleaseDailyLockers(ctx)
	return err
}

// RegisterJobs adds the golf workers and schedules.
func (m *Module) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &HoldsWorker{M: m})
	river.AddWorker(reg.Workers, &NoShowWorker{M: m})
	river.AddWorker(reg.Workers, &TeeSheetWorker{M: m})
	river.AddWorker(reg.Workers, &ReminderWorker{M: m})
	river.AddWorker(reg.Workers, &LockerWorker{M: m})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) { return HoldsArgs{}, nil }, nil),
		river.NewPeriodicJob(river.PeriodicInterval(10*time.Minute), func() (river.JobArgs, *river.InsertOpts) { return NoShowArgs{}, nil }, nil),
		river.NewPeriodicJob(jobs.DailyAt{Hour: 0, Minute: 15, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return TeeSheetArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(jobs.DailyAt{Hour: 8, Minute: 0, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return ReminderArgs{}, nil }, nil),
		// daily lockers of players who left without a check-out
		river.NewPeriodicJob(jobs.DailyAt{Hour: 0, Minute: 5, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return LockerArgs{}, nil }, nil))
}

// ── subscribers ───────────────────────────────────────────────────────────

// OnPaymentSettled confirms pending bookings once paid (or the deposit is
// held); a payment arriving for an already cancelled booking is refunded.
func (m *Module) OnPaymentSettled(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p billing.SettledPayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	if p.SourceType == nil || *p.SourceType != "golf_booking" || p.SourceID == nil || p.FolioID == nil || e.PropertyID == nil {
		return nil
	}
	bid, err := uuid.Parse(*p.SourceID)
	if err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), *e.PropertyID)
	var status string
	var mode, deposit *string
	if err := tx.QueryRow(ctx, `SELECT status, payment_mode, deposit_amount::text FROM golf.bookings WHERE id = $1 FOR UPDATE`, bid).Scan(&status, &mode, &deposit); err != nil {
		return err
	}
	switch status {
	case "pending":
		paid, held, err := billing.PaidTowards(ctx, tx, *p.FolioID)
		if err != nil {
			return err
		}
		sum, err := billing.FolioSummary(ctx, tx, *p.FolioID)
		if err != nil {
			return err
		}
		charges := dec(sum.Charges)
		ok := paid.GreaterThanOrEqual(charges)
		if mode != nil && *mode == "deposit" && deposit != nil && held.Add(paid).GreaterThanOrEqual(dec(*deposit)) {
			ok = true
		}
		if !ok {
			return nil
		}
		if err := m.confirm(ctx, tx, *e.PropertyID, bid, "payment "+p.Number+" settled"); err != nil {
			if isHoldExpired(err) {
				// seats lapsed while paying: cancel and refund in full
				_, cerr := m.Cancel(ctx, tx, *e.PropertyID, bid, CancelRequest{Reason: "hold expired before payment settled"})
				return cerr
			}
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "confirm", Category: audit.CategorySystem, EntityType: "golf.booking",
			EntityID: bid.String(), PropertyID: e.PropertyID, ActorName: "payment " + p.Number})
	case "cancelled", "no_show":
		_, err := m.Billing.RefundFolio(ctx, tx, *p.FolioID, "late payment for a cancelled booking", m.Refunds)
		return err
	}
	return nil
}

func isHoldExpired(err error) bool {
	e, ok := errs.As(err)
	return ok && e.Code == "hold_expired"
}

// OnGuestUpgraded points the guest's bookings at the new customer so the
// history follows the upgrade (FR-CUS-02).
func (m *Module) OnGuestUpgraded(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p crm.UpgradedPayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET customer_id = $2 WHERE guest_id = $1 AND customer_id IS NULL`, p.GuestID, p.CustomerID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE golf.bookings SET customer_id = $2 WHERE guest_id = $1 AND customer_id IS NULL`, p.GuestID, p.CustomerID)
	return err
}

// OnCustomerMerged moves golf references to the kept profile (FR-CUS-03).
func (m *Module) OnCustomerMerged(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p crm.MergedPayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	for _, s := range []string{
		`UPDATE golf.bookings SET customer_id = $2 WHERE customer_id = $1`,
		`UPDATE golf.booking_players SET customer_id = $2 WHERE customer_id = $1`,
		`UPDATE golf.handicaps SET customer_id = $2 WHERE customer_id = $1`,
		`UPDATE golf.rain_checks SET customer_id = $2 WHERE customer_id = $1`,
		`UPDATE golf.locker_assignments SET customer_id = $2 WHERE customer_id = $1`,
		`UPDATE golf.bag_storage SET customer_id = $2 WHERE customer_id = $1`,
	} {
		if _, err := tx.Exec(ctx, s, p.SourceID, p.TargetID); err != nil {
			return err
		}
	}
	return nil
}

// OnCustomerErased removes personal data copied into golf records.
func (m *Module) OnCustomerErased(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p crm.ErasedPayload
	if err := e.Decode(&p); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET contact_name = 'Erased Customer', contact_phone = NULL, contact_email = NULL WHERE customer_id = $1`, p.CustomerID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE golf.booking_players SET name = 'Erased Customer', phone = NULL WHERE customer_id = $1`, p.CustomerID)
	return err
}
