package app

// Wiring of PRD P5 People & Advanced Enterprise (hris, advanced CRM &
// loyalty, BI, advanced package & tournament) after P4 (PRD P5 §5.4:
// internal/app is shared and changes additively). Each area wires itself
// in p5_<area>.go; this file only aggregates them.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// P5 holds the P5 services.
type P5 struct {
	HR      p5HR      // EP-01–05, EP-16, EP-24 (core HR, ESS)
	Time    p5HRTime  // EP-06–08 (schedules, attendance, leave & overtime)
	Payroll p5Payroll // EP-09–15 (payroll, statutory, service charge, payouts)
	CRMPlus p5CRM     // EP-17–20 (advanced segmentation, loyalty, journeys, analytics)
	BI      p5BI      // EP-21, EP-27 (analytics store, executive dashboard, HR KPI)
	Leisure p5Leisure // EP-22–23 (advanced package & tournament)
}

func p5Contributions() []catalog.Contribution {
	var out []catalog.Contribution
	for _, c := range [][]catalog.Contribution{p5HRContributions(), p5HRTimeContributions(), p5PayrollContributions(), p5CRMContributions(),
		p5BIContributions(), p5LeisureContributions()} {
		out = append(out, c...)
	}
	return out
}

func p5DocumentTypes() []provision.DocumentType {
	var out []provision.DocumentType
	for _, d := range [][]provision.DocumentType{p5HRDocumentTypes(), p5HRTimeDocumentTypes(), p5PayrollDocumentTypes(), p5CRMDocumentTypes(),
		p5BIDocumentTypes(), p5LeisureDocumentTypes()} {
		out = append(out, d...)
	}
	return out
}

func p5Templates() []provision.Template {
	var out []provision.Template
	for _, t := range [][]provision.Template{p5HRTemplates(), p5HRTimeTemplates(), p5PayrollTemplates(), p5CRMTemplates(), p5BITemplates(),
		p5LeisureTemplates()} {
		out = append(out, t...)
	}
	return out
}

// buildP5 wires the P5 services and routes after P4's.
func (a *App) buildP5(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	a.buildP5HR(reg, cfg, db, files)
	a.buildP5HRTime(reg, cfg, db, files)
	a.buildP5Payroll(reg, cfg, db, files)
	a.buildP5CRM(reg, cfg, db, files)
	a.buildP5BI(reg, cfg, db, files)
	a.buildP5Leisure(reg, cfg, db, files)
}

// subscribeP5 registers the P5 event subscribers.
func (a *App) subscribeP5() {
	a.subscribeP5HR()
	a.subscribeP5HRTime()
	a.subscribeP5Payroll()
	a.subscribeP5CRM()
	a.subscribeP5BI()
	a.subscribeP5Leisure()
}

// seedP5Demo seeds the demo data of the P5 areas.
func seedP5Demo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	for _, f := range []func(context.Context, pgx.Tx, uuid.UUID) error{demoP5HR, demoP5HRTime, demoP5Payroll, demoP5CRM, demoP5BI, demoP5Leisure} {
		if err := f(ctx, tx, property); err != nil {
			return err
		}
	}
	return nil
}
