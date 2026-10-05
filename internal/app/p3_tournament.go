package app

// PRD P3 EP-16 Tournament Management (owner: golf/tournament): routes of
// the Back Office, Tournament Desk, Caddy Tablet, Member App and website;
// the fee waiver approval; the offline queue actions of the scoring desk and
// the caddy tablet (FR-OPS-P3-05); the Customer 360 Tournament section;
// conversion of accepted "tournament" quotations (FR-QUO-06, idempotent per
// quotation) and the banquet event snapshot (FR-TRN-13) through events.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/golf/tournament"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// p3Tournament holds the services of the area.
type p3Tournament struct {
	Module *tournament.Module
}

// p3TournamentContributions are the catalogue contributions (permissions, role mappings).
func p3TournamentContributions() []catalog.Contribution {
	return []catalog.Contribution{tournament.Contribution()}
}

// p3TournamentDocumentTypes are the approval document types.
func p3TournamentDocumentTypes() []provision.DocumentType { return tournament.DocumentTypes() }

// p3TournamentTemplates are the notification templates.
func p3TournamentTemplates() []provision.Template { return tournament.Templates() }

// buildP3Tournament wires routes, hooks, approval decisions and jobs.
func (a *App) buildP3Tournament(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	m := &tournament.Module{DB: db, Events: a.Bus, Approvals: a.Approvals, Billing: a.Billing, Invoices: a.BillingHTTP, Refunds: a.BillingHTTP,
		Notify: a.Notification, Hub: a.Hub, Golf: a.Golf, Experience: a.Experience, Integrations: a.Integrations, Cfg: cfg}
	m.RegisterRoutes(reg)
	a.Approvals.RegisterDocumentType(tournament.FeeWaiverType, m.Decision)
	a.Sync.Handle("golf.tournament_score", m.SyncScoreHandler)
	a.Sync.Handle("golf.tournament_check_in", m.SyncCheckInHandler)
	m.RegisterJobs(a.Registrar)
	if a.CRM != nil && a.CRM.Sections != nil {
		a.CRM.Sections["tournament"] = m.CustomerSection
	}
	a.corporateSection("tournament", m.CorporateSection) // FR-C360-04
	a.Tournament.Module = m
	a.convertsQuotations("golf", "tournament")
}

// subscribeP3Tournament registers the event subscribers.
func (a *App) subscribeP3Tournament() {
	m := a.Tournament.Module
	a.Bus.Subscribe(billing.EventPaymentSettled, "golf.tournament_payment", func(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
		var p billing.SettledPayload
		if err := e.Decode(&p); err != nil {
			return nil //nolint:nilerr // foreign payload
		}
		if p.SourceType == nil || *p.SourceType != "tournament" || e.PropertyID == nil {
			return nil
		}
		return m.OnPaymentSettled(reqctx.WithProperty(dbtx.System(ctx), *e.PropertyID), tx, p)
	})
	a.Bus.Subscribe(tournament.EventQuotationAccepted, "golf.tournament_quotation", m.OnQuotationAccepted)
	a.Bus.Subscribe(tournament.EventBanquetConfirmed, "golf.tournament_event_confirmed", m.OnBanquetEvent)
	a.Bus.Subscribe(tournament.EventBanquetCancelled, "golf.tournament_event_cancelled", m.OnBanquetEvent)
}

// demoP3Tournament seeds demo data for the area on the MAIN property
// (idempotent): the draft Monthly Medal on the championship course.
func demoP3Tournament(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournaments WHERE property_id = $1 AND code = 'DEMO-MEDAL')`, property).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var course uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM golf.courses WHERE property_id = $1 AND code = $2`, property, DemoCourseCode).Scan(&course)
	if dbtx.IsNoRows(err) {
		return nil // golf demo not seeded
	}
	if err != nil {
		return err
	}
	ctx = reqctx.WithProperty(ctx, property)
	// second Saturday of next month (club calendar, PRD P3 §16 #12)
	now := time.Now()
	d := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	for d.Weekday() != time.Saturday {
		d = d.AddDate(0, 0, 1)
	}
	d = d.AddDate(0, 0, 7)
	m := &tournament.Module{}
	t, err := m.Create(ctx, tx, property, tournament.TournamentInput{Code: "DEMO-MEDAL", Name: "Monthly Medal", TournamentType: "club", CourseID: course,
		Format: "stableford", Eligibility: "members_and_guests", FieldSize: 72, StartType: "shotgun", Public: true,
		Rounds: []tournament.TournamentRoundInput{{PlayDate: d.Format("2006-01-02"), StartTime: "07:00"}}})
	if err != nil {
		return err
	}
	for i, div := range []struct{ code, name, lo, hi string }{{"A", "Flight A", "0", "12.4"}, {"B", "Flight B", "12.5", "22.4"}, {"C", "Flight C", "22.5", "54"}} {
		code, name, lo, hi, seq := div.code, div.name, div.lo, div.hi, i+1
		if _, err := m.SaveDivision(ctx, tx, property, t.ID, nil, tournament.TournamentDivisionInput{Code: &code, Name: &name, HandicapMin: &lo, HandicapMax: &hi,
			Sequence: &seq}); err != nil {
			return err
		}
	}
	entry, name, member, guest := "entry_fee", "Tournament Fee", "member", "guest"
	for _, f := range []struct{ typ, amount string }{{member, "350000"}, {guest, "750000"}} {
		typ, amount := f.typ, f.amount
		if _, err := m.SaveFee(ctx, tx, property, t.ID, nil, tournament.TournamentFeeInput{Component: &entry, Name: &name, PlayerType: &typ, Amount: &amount}); err != nil {
			return err
		}
	}
	return nil
}
