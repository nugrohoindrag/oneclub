package sportclub

// Jobs of the court booking: the in-app reminder before each booked line
// (FR-59, every minute) and the membership prospects among frequent
// players (FR-89, hourly).

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/provision"
)

// CourtRemindersArgs sends the court reminders due.
type CourtRemindersArgs struct{}

func (CourtRemindersArgs) Kind() string { return "sportclub_court_reminders" }

func (CourtRemindersArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type courtRemindersWorker struct {
	river.WorkerDefaults[CourtRemindersArgs]
	m *Module
}

func (w *courtRemindersWorker) Work(ctx context.Context, _ *river.Job[CourtRemindersArgs]) error {
	_, err := w.m.SendReminders(ctx)
	return err
}

// ProspectsArgs flags the membership prospects.
type ProspectsArgs struct{}

func (ProspectsArgs) Kind() string { return "sportclub_membership_prospects" }

func (ProspectsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type prospectsWorker struct {
	river.WorkerDefaults[ProspectsArgs]
	m *Module
}

func (w *prospectsWorker) Work(ctx context.Context, _ *river.Job[ProspectsArgs]) error {
	_, err := w.m.FlagProspects(ctx)
	return err
}

// RegisterJobs adds the court booking jobs.
func (m *Module) RegisterJobs(reg *jobs.Registrar) {
	river.AddWorker(reg.Workers, &courtRemindersWorker{m: m})
	river.AddWorker(reg.Workers, &prospectsWorker{m: m})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) { return CourtRemindersArgs{}, nil }, nil),
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) { return ProspectsArgs{}, nil }, nil))
}

// courtTemplates are the in-app messages of the court operation.
func courtTemplates() []provision.Template {
	t := map[string]map[string][2]string{
		"sportclub.court_reminder": {
			"id": {"{{.court}} {{.time}}: siapkan lapangan", "Booking {{.code}} ({{.name}}) mulai {{.time}} di {{.court}}. Siapkan lapangan dan nyalakan lampu."},
			"en": {"{{.court}} {{.time}}: prepare the court", "Booking {{.code}} ({{.name}}) starts at {{.time}} on {{.court}}. Prepare the court and switch the lights on."},
		},
		"sportclub.court_issue": {
			"id": {"Masalah lapangan: {{.court}}", "{{.court}}: {{.note}}. Lapangan ditutup sementara; {{.affected}} booking perlu dipindah."},
			"en": {"Court problem: {{.court}}", "{{.court}}: {{.note}}. The court is closed for a while; {{.affected}} booking(s) to move."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			out = append(out, provision.Template{Event: ev, Channel: "in_app", Locale: loc, Subject: c[0], Body: c[1]})
		}
	}
	return out
}
