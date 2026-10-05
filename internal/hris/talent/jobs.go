package talent

// Daily job of recruitment and performance review (00:20 instance time, per
// property in its time zone): erase the applicants whose retention period
// ended (FR-RCT-06, §16 #10), expire offers not answered in time and remind
// employees and managers of review steps due soon.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/jobs"
)

// DailyArgs runs the daily recruitment & review job.
type DailyArgs struct{}

func (DailyArgs) Kind() string { return "hris_talent_daily" }
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
	Erased          int `json:"erased"`
	OffersExpired   int `json:"offersExpired"`
	ReviewReminders int `json:"reviewReminders"`
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
		if err := m.DB.WithTx(pctx, func(tx pgx.Tx) error { return m.dailyProperty(pctx, tx, p, &rep) }); err != nil {
			return rep, fmt.Errorf("hris talent daily for %s: %w", p, err)
		}
	}
	return rep, nil
}

func (m *Module) dailyProperty(ctx context.Context, tx pgx.Tx, property uuid.UUID, rep *DailyReport) error {
	day := today(ctx, tx, property)
	// Applicants past their retention period (no open or hired application).
	rows, err := tx.Query(ctx, `SELECT c.id FROM hris.candidates c WHERE c.property_id = $1 AND c.erased_at IS NULL AND c.status = 'active'
		AND c.retention_until IS NOT NULL AND c.retention_until < $2::date
		AND NOT EXISTS (SELECT 1 FROM hris.applications a WHERE a.candidate_id = c.id AND a.stage IN ('applied', 'screening', 'interview', 'offered', 'hired'))
		ORDER BY c.retention_until`, property, ymd(day))
	if err != nil {
		return err
	}
	due, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, cid := range due {
		if err := m.erase(ctx, tx, cid, "Retention period ended (UU PDP)"); err != nil {
			return err
		}
		rep.Erased++
	}
	// Offers not answered in time.
	rows, err = tx.Query(ctx, `SELECT o.id, a.id, a.property_id, a.number, a.requisition_id, a.candidate_id, a.stage FROM hris.job_offers o
		JOIN hris.applications a ON a.id = o.application_id WHERE o.property_id = $1 AND o.status = 'sent' AND o.expires_on < $2::date`, property, ymd(day))
	if err != nil {
		return err
	}
	type expired struct {
		offer uuid.UUID
		app   JobApplication
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (expired, error) {
		var x expired
		err := row.Scan(&x.offer, &x.app.ID, &x.app.PropertyID, &x.app.Number, &x.app.RequisitionID, &x.app.CandidateID, &x.app.Stage)
		return x, err
	})
	if err != nil {
		return err
	}
	for _, x := range list {
		if _, err := tx.Exec(ctx, `UPDATE hris.job_offers SET status = 'expired', decision_note = 'Not answered before the expiry date' WHERE id = $1`,
			x.offer); err != nil {
			return err
		}
		if x.app.Stage == hris.StageOffered {
			if err := m.CloseApplication(ctx, tx, x.app, hris.StageWithdrawn, "Offer expired without an answer", false); err != nil {
				return err
			}
		}
		rep.OffersExpired++
	}
	// Review reminders N days before the self / manager due dates.
	cfg, _, err := hris.LoadPerformanceConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT id, name, self_due, manager_due FROM hris.review_cycles WHERE property_id = $1 AND status = 'in_progress'`, property)
	if err != nil {
		return err
	}
	type cyc struct {
		id                  uuid.UUID
		name                string
		selfDue, managerDue *time.Time
	}
	cycles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (cyc, error) {
		var c cyc
		return c, row.Scan(&c.id, &c.name, &c.selfDue, &c.managerDue)
	})
	if err != nil {
		return err
	}
	for _, c := range cycles {
		for _, step := range []struct {
			due    *time.Time
			status string
			who    string
		}{{c.selfDue, "self_assessment", "u.employee_id = r.employee_id"}, {c.managerDue, "manager_review", "u.employee_id = r.reviewer_id"}} {
			if step.due == nil || !slices.Contains(cfg.ReminderDays, int(step.due.Sub(day).Hours()/24)) {
				continue
			}
			rows, err := tx.Query(ctx, `SELECT u.id, count(*)::int FROM hris.performance_reviews r JOIN platform.users u ON `+step.who+` AND u.status = 'active'
				WHERE r.cycle_id = $1 AND r.status = $2 GROUP BY u.id`, c.id, step.status)
			if err != nil {
				return err
			}
			type pending struct {
				user uuid.UUID
				n    int
			}
			ps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pending, error) {
				var p pending
				return p, row.Scan(&p.user, &p.n)
			})
			if err != nil {
				return err
			}
			for _, p := range ps {
				link := "/ops/ess/reviews"
				if step.status == "manager_review" {
					link = "/ops/ess/team-reviews"
				}
				if err := m.notifyUsers(ctx, tx, property, []uuid.UUID{p.user}, "hris.review_reminder", link, map[string]any{"cycle": c.name,
					"count": p.n, "dueDate": ymd(*step.due)}); err != nil {
					return err
				}
				rep.ReviewReminders++
			}
		}
	}
	return nil
}

// jsonOf marshals a value for a jsonb column.
func jsonOf(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}
