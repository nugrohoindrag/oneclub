package app

// PRD P5 EP-21 and EP-27: analytics store, executive dashboard with targets, drill-down, scheduled & self-service reports, HR KPI & reports. internal/app/p5.go calls these functions; the area fills them.

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
	"oneclub/internal/reporting"
)

// p5BI holds the services of the area.
type p5BI struct {
	Svc *reporting.BI
}

// The reports registered by the P5 areas (reporting.RegisterP5Report from
// their init) join the report registry, the catalogue sync and the export.
func init() {
	Reports = append(Reports, reporting.P5Reports()...)
}

func p5BIContributions() []catalog.Contribution {
	return []catalog.Contribution{reporting.P5Contribution()}
}

func p5BIDocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{reporting.KPITargetDocumentType}
}

func p5BITemplates() []provision.Template { return reporting.ScheduledReportTemplates() }

// buildP5BI wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5BI(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	b := &reporting.BI{S: a.Reporting, Approvals: a.Approvals, Modules: a.Instance, LoadAuthz: a.LoadPrincipal}
	b.Register(reg)
	a.Approvals.RegisterDocumentType(reporting.KPITargetDocumentType, b.KPITargetDecision)
	b.RegisterJobs(a.Registrar, a.Instance.Location)
	a.BI.Svc = b
}

// subscribeP5BI registers the event subscribers (none: the analytics store
// is refreshed by its jobs every 10 minutes).
func (a *App) subscribeP5BI() {}

// demoP5BI seeds the demo data of the area: the approved KPI target plan of
// the current year (annual budget per month, PRD P5 §16 #13), three
// scheduled reports for management and a first materialisation of the
// analytics store for the month to date.
func demoP5BI(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	loc := calendar.Location(ctx, tx)
	now := time.Now().In(loc)
	year := now.Year()
	plan := id.New()
	tag, err := tx.Exec(ctx, `INSERT INTO reporting.kpi_target_plans (id, property_id, year, version, title, notes, status, submitted_at, decided_at)
		VALUES ($1, $2, $3, 1, $4, 'Annual budget approved by the Board, split per month', 'approved', now(), now())
		ON CONFLICT (property_id, year, version) DO NOTHING`, plan, property, year, "Budget "+strconv.Itoa(year))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		annual := []reporting.KPITargetAnnual{
			{KPIKey: "golf_revenue", Amount: "9000000000"}, {KPIKey: "golf_rounds", Amount: "42000"},
			{KPIKey: "tee_time_utilization", Amount: "0.65"}, {KPIKey: "caddy_utilization", Amount: "0.55"},
			{KPIKey: "sport_revenue", Amount: "2400000000"}, {KPIKey: "sport_entries", Amount: "60000"}, {KPIKey: "court_utilization", Amount: "0.45"},
			{KPIKey: "active_members", Amount: "1200"}, {KPIKey: "new_members", Amount: "120"}, {KPIKey: "member_revenue", Amount: "6000000000"},
			{KPIKey: "bookings", Amount: "30000"}, {KPIKey: "bungalow_occupancy", Amount: "0.6"}, {KPIKey: "booking_cancellation", Amount: "1200"},
			{KPIKey: "banquet_revenue", Amount: "7200000000"}, {KPIKey: "banquet_events", Amount: "180"},
			{KPIKey: "total_sales", Amount: "36000000000"}, {KPIKey: "pos_sales", Amount: "8400000000"}, {KPIKey: "food_cost_percent", Amount: "0.32"},
			{KPIKey: "inventory_value", Amount: "850000000"}, {KPIKey: "purchase_value", Amount: "6000000000"}, {KPIKey: "vendor_on_time", Amount: "0.9"},
			{KPIKey: "gl_revenue", Amount: "36000000000"}, {KPIKey: "net_income", Amount: "5400000000"}, {KPIKey: "accounts_receivable", Amount: "1500000000"},
			{KPIKey: "cash", Amount: "3000000000"}, {KPIKey: "nps", Amount: "50"}, {KPIKey: "complaint_sla", Amount: "0.9"}, {KPIKey: "leads", Amount: "1800"},
			{KPIKey: "deals_won", Amount: "12000000000"}, {KPIKey: "headcount", Amount: "180"},
		}
		if err := reporting.SeedTargets(ctx, tx, plan, property, annual); err != nil {
			return err
		}
	}
	schedules := []struct {
		name, report, format, period, frequency string
		weekday, monthDay                       *int
		at                                      string
		roles                                   []string
	}{
		{"Weekly Management Pack", "reporting.kpi_target_vs_actual", "pdf", "month_to_date", "weekly", biInt(1), nil, "07:00",
			[]string{"general_manager", "finance_manager"}},
		{"Daily Revenue", "billing.daily_revenue", "xlsx", "previous_day", "daily", nil, nil, "06:30", []string{"finance_manager"}},
		{"Monthly Profit & Loss", "accounting.profit_loss", "pdf", "previous_month", "monthly", nil, biInt(3), "08:00",
			[]string{"general_manager", "finance_manager"}},
	}
	for _, s := range schedules {
		next := reporting.NextScheduledRun(s.frequency, s.weekday, s.monthDay, s.at, now, loc)
		if _, err := tx.Exec(ctx, `INSERT INTO reporting.scheduled_reports (id, property_id, name, report_code, format, period, frequency, weekday, month_day,
			send_time, channels, recipient_roles, status, next_run_at)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10::time, '{email,in_app}', $11, 'active', $12
			WHERE NOT EXISTS (SELECT 1 FROM reporting.scheduled_reports WHERE property_id = $2 AND name = $3)`,
			id.New(), property, s.name, s.report, s.format, s.period, s.frequency, s.weekday, s.monthDay, s.at, s.roles, next); err != nil {
			return err
		}
	}
	return (&reporting.BI{}).SeedRefresh(ctx, tx, property)
}

func biInt(n int) *int { return &n }
