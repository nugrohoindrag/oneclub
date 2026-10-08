package experience

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/provision"
)

// PaceCheckArgs flags slow flights every minute (FR-PLX-04: ≤ 1 minute).
type PaceCheckArgs struct{}

func (PaceCheckArgs) Kind() string { return "golf_pace_check" }

func (PaceCheckArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 1}
}

type PaceCheckWorker struct {
	river.WorkerDefaults[PaceCheckArgs]
	M *Module
}

func (w *PaceCheckWorker) Work(ctx context.Context, _ *river.Job[PaceCheckArgs]) error {
	_, err := w.M.RunPaceCheck(ctx)
	return err
}

// RunPaceCheck checks every property with flights on course.
func (m *Module) RunPaceCheck(ctx context.Context) (int, error) {
	sys := dbtx.System(ctx)
	n := 0
	err := m.DB.WithTx(sys, func(tx pgx.Tx) error {
		rows, err := tx.Query(sys, `SELECT DISTINCT property_id FROM golf.flights WHERE status = 'in_play'`)
		if err != nil {
			return err
		}
		props, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		for _, p := range props {
			k, err := m.CheckPace(sys, tx, p)
			if err != nil {
				return err
			}
			n += k
		}
		return nil
	})
	return n, err
}

// RegisterJobs adds the golf workers and schedules.
func (m *Module) RegisterJobs(reg *jobs.Registrar) {
	river.AddWorker(reg.Workers, &PaceCheckWorker{M: m})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return PaceCheckArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}))
	river.AddWorker(reg.Workers, &RangeHoldWorker{M: m})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(river.PeriodicInterval(5*time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return RangeHoldArgs{}, nil }, nil))
}

// Templates are the golf notification templates (FR-INT-P2-04).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"golf.caddy_next_assignment": {
			"en": {"Next assignment: {{.flight}} {{.teeTime}}", "You are assigned to flight {{.flight}} ({{.route}}) teeing off at {{.teeTime}}. Please accept on the tablet."},
			"id": {"Penugasan berikutnya: {{.flight}} {{.teeTime}}", "Anda ditugaskan ke flight {{.flight}} ({{.route}}) tee off pukul {{.teeTime}}. Mohon terima di tablet."},
		},
		"golf.cart_service_due": {
			"en": {"Golf cart {{.cart}} is due for service", "Golf cart {{.cart}} has {{.hours}} usage hours since its last service (threshold {{.threshold}})."},
			"id": {"Golf cart {{.cart}} perlu servis", "Golf cart {{.cart}} sudah {{.hours}} jam pemakaian sejak servis terakhir (ambang {{.threshold}})."},
		},
		"golf.introduction_letter_issued": {
			"en": {"Your introduction letter to {{.club}}", "Hello {{.name}},\n\nYour introduction letter to {{.club}} for {{.from}} – {{.to}} has been issued. You can download it in the Member Portal."},
			"id": {"Surat pengantar ke {{.club}}", "Halo {{.name}},\n\nSurat pengantar Anda ke {{.club}} untuk {{.from}} – {{.to}} telah terbit. Unduh di Member Portal."},
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
