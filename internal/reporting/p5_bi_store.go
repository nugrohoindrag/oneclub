package reporting

// Analytics store (FR-BI-01): jobs materialise the executive KPIs per
// property into analytics.kpi_values (day, month and year grain) by running
// the KPI definitions of the domain dashboards, and the revenue facts per
// day and dimension into analytics.revenue_daily. The incremental refresh
// runs every 10 minutes over the last days (data latency ≤ 15 minutes,
// PRD P5 §12); the nightly refresh recomputes the backfill window of the
// BI Policies; Finance can refresh on demand.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
)

// AnalyticsRefreshRun is one refresh of the analytics store.
type AnalyticsRefreshRun struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	PropertyID    uuid.UUID  `json:"propertyId" db:"property_id"`
	Kind          string     `json:"kind" db:"kind" enum:"incremental,backfill,manual,seed"`
	WindowFrom    string     `json:"windowFrom" db:"window_from"`
	WindowTo      string     `json:"windowTo" db:"window_to"`
	Status        string     `json:"status" db:"status" enum:"running,completed,failed"`
	KPIs          int        `json:"kpis" db:"kpis"`
	ValuesWritten int        `json:"valuesWritten" db:"values_written"`
	FactsWritten  int        `json:"factsWritten" db:"facts_written"`
	DurationMS    *int       `json:"durationMs" db:"duration_ms"`
	Error         *string    `json:"error" db:"error"`
	StartedAt     time.Time  `json:"startedAt" db:"started_at"`
	FinishedAt    *time.Time `json:"finishedAt" db:"finished_at"`
}

// AnalyticsStatus is the freshness of the analytics store at a property.
type AnalyticsStatus struct {
	DataAsOf          *time.Time            `json:"dataAsOf" doc:"Oldest refresh time of the current month's KPI values"`
	LatencyMinutes    *int                  `json:"latencyMinutes"`
	Stale             bool                  `json:"stale"`
	StaleAfterMinutes int                   `json:"staleAfterMinutes"`
	Runs              []AnalyticsRefreshRun `json:"runs"`
}

// AnalyticsRefreshRequest refreshes a window (default: the incremental
// window of the BI Policies).
type AnalyticsRefreshRequest struct {
	From string `json:"from,omitempty" doc:"YYYY-MM-DD"`
	To   string `json:"to,omitempty" doc:"YYYY-MM-DD"`
}

const dateFmt = "2006-01-02"

func day(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func monthEnd(t time.Time) time.Time { return monthStart(t).AddDate(0, 1, -1) }

func minDate(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

type kpiValue struct {
	key, grain string
	start, end time.Time
	value      string
}

// refreshProperty materialises the window [from, to] of one property in
// tx, whose RLS scope must be that property. It records the run.
func (b *BI) refreshProperty(ctx context.Context, tx pgx.Tx, property uuid.UUID, kind string, from, to time.Time, by *uuid.UUID) (AnalyticsRefreshRun, error) {
	started := time.Now()
	loc := calendar.Location(ctx, tx)
	tz := loc.String()
	today := day(b.now().In(loc))
	from, to = day(from), day(to)
	if to.After(today) {
		to = today
	}
	if from.After(to) {
		from = to
	}
	run := AnalyticsRefreshRun{ID: id.New(), PropertyID: property, Kind: kind, WindowFrom: from.Format(dateFmt), WindowTo: to.Format(dateFmt), Status: "running",
		StartedAt: started.UTC()}
	// one refresh per property at a time (job, nightly and manual runs)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('analytics.refresh:' || $1::text))`, property); err != nil {
		return run, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('statement_timeout', '300000', true)`); err != nil {
		return run, err
	}
	var values []kpiValue
	var failed []string
	kpis := executiveKPIs()
	for _, k := range kpis {
		if k.SQL == "" {
			continue
		}
		run.KPIs++
		eval := func(f, t time.Time) (string, error) {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return "", err
			}
			var v *string
			if err := sp.QueryRow(ctx, k.SQL, f.Format(dateFmt), t.Format(dateFmt), tz).Scan(&v); err != nil {
				_ = sp.Rollback(ctx)
				return "", err
			}
			if err := sp.Commit(ctx); err != nil {
				return "", err
			}
			if v == nil || *v == "" {
				return "0", nil
			}
			return *v, nil
		}
		var kv []kpiValue
		err := func() error {
			if k.Kind == KindCurrent {
				// the state now: only today's rows (history accrues day by day)
				if to.Before(today) {
					return nil
				}
				v, err := eval(today, today)
				if err != nil {
					return err
				}
				kv = append(kv, kpiValue{k.Key, "day", today, today, v}, kpiValue{k.Key, "month", monthStart(today), today, v},
					kpiValue{k.Key, "year", time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC), today, v})
				return nil
			}
			for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
				v, err := eval(d, d)
				if err != nil {
					return err
				}
				kv = append(kv, kpiValue{k.Key, "day", d, d, v})
			}
			for m := monthStart(from); !m.After(to); m = m.AddDate(0, 1, 0) {
				end := minDate(monthEnd(m), today)
				v, err := eval(m, end)
				if err != nil {
					return err
				}
				kv = append(kv, kpiValue{k.Key, "month", m, end, v})
			}
			for y := from.Year(); y <= to.Year(); y++ {
				start := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
				end := minDate(time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC), today)
				v, err := eval(start, end)
				if err != nil {
					return err
				}
				kv = append(kv, kpiValue{k.Key, "year", start, end, v})
			}
			return nil
		}()
		if err != nil {
			failed = append(failed, k.Key+": "+err.Error())
			slog.WarnContext(ctx, "analytics: kpi refresh failed", "kpi", k.Key, "property", property, "err", err)
			continue
		}
		values = append(values, kv...)
	}
	if len(values) > 0 {
		keys, grains, starts, ends, vals := make([]string, len(values)), make([]string, len(values)), make([]string, len(values)),
			make([]string, len(values)), make([]string, len(values))
		for i, v := range values {
			keys[i], grains[i], starts[i], ends[i], vals[i] = v.key, v.grain, v.start.Format(dateFmt), v.end.Format(dateFmt), v.value
		}
		if _, err := tx.Exec(ctx, `INSERT INTO analytics.kpi_values (property_id, kpi_key, grain, period_start, period_end, value, refreshed_at)
			SELECT $1, k, g, s::date, e::date, v::numeric, now() FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[]) AS x(k, g, s, e, v)
			ON CONFLICT (property_id, kpi_key, grain, period_start) DO UPDATE SET period_end = EXCLUDED.period_end, value = EXCLUDED.value,
			  refreshed_at = EXCLUDED.refreshed_at`, property, keys, grains, starts, ends, vals); err != nil {
			return run, err
		}
		run.ValuesWritten = len(values)
	}
	// revenue facts of the window (drill-down dimensions)
	if _, err := tx.Exec(ctx, `DELETE FROM analytics.revenue_daily WHERE property_id = $1 AND day BETWEEN $2::date AND $3::date`,
		property, from.Format(dateFmt), to.Format(dateFmt)); err != nil {
		return run, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO analytics.revenue_daily (property_id, day, daypart, business_line, revenue_component, outlet, segment, amount, lines,
		refreshed_at)
		SELECT property_id, (posted_at AT TIME ZONE $4)::date, CASE WHEN extract(hour FROM posted_at AT TIME ZONE $4) < 12 THEN 'morning'
		  WHEN extract(hour FROM posted_at AT TIME ZONE $4) < 17 THEN 'afternoon' ELSE 'evening' END,
		  business_line, revenue_component, outlet, segment, sum(total), count(*), now()
		FROM reporting.bi_revenue_lines WHERE NOT liability AND property_id = $1
		  AND posted_at >= ($2::date::timestamp AT TIME ZONE $4) AND posted_at < (($3::date + 1)::timestamp AT TIME ZONE $4)
		GROUP BY 1, 2, 3, 4, 5, 6, 7`, property, from.Format(dateFmt), to.Format(dateFmt), tz)
	if err != nil {
		return run, err
	}
	run.FactsWritten = int(tag.RowsAffected())
	ms := int(time.Since(started).Milliseconds())
	run.DurationMS = &ms
	run.Status = "completed"
	if len(failed) > 0 {
		msg := strings.Join(failed, "; ")
		if len(msg) > 2000 {
			msg = msg[:2000]
		}
		run.Error = &msg
	}
	now := time.Now().UTC()
	run.FinishedAt = &now
	_, err = tx.Exec(ctx, `INSERT INTO analytics.refresh_runs (id, property_id, kind, window_from, window_to, status, kpis, values_written, facts_written,
		duration_ms, error, requested_by, started_at, finished_at) VALUES ($1,$2,$3,$4::date,$5::date,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		run.ID, property, kind, run.WindowFrom, run.WindowTo, run.Status, run.KPIs, run.ValuesWritten, run.FactsWritten, ms, run.Error, by, run.StartedAt, now)
	return run, err
}

// Refresh refreshes every active property (job). kind is incremental or
// backfill; the window comes from each property's BI Policies.
func (b *BI) Refresh(ctx context.Context, kind string) (int, error) {
	sys := dbtx.System(ctx)
	var props []uuid.UUID
	if err := b.S.DB.WithReadTx(sys, func(tx pgx.Tx) error {
		rows, err := tx.Query(sys, `SELECT id FROM platform.properties WHERE status = 'active' AND archived_at IS NULL ORDER BY created_at`)
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
		pctx := authz.WithPrincipal(dbtx.WithScope(ctx, dbtx.Scope{PropertyIDs: []uuid.UUID{p}}), authz.System())
		err := b.S.DB.WithTx(pctx, func(tx pgx.Tx) error {
			pol := LoadBIPolicy(pctx, tx, p)
			days := pol.RefreshWindowDays
			if kind == "backfill" {
				days = pol.BackfillDays
			}
			today := b.now().In(calendar.Location(pctx, tx))
			_, err := b.refreshProperty(pctx, tx, p, kind, today.AddDate(0, 0, -(days-1)), today, nil)
			return err
		})
		if err != nil {
			slog.ErrorContext(ctx, "analytics refresh failed", "property", p, "kind", kind, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

// SeedRefresh materialises the current year to date of a property inside
// the demo seed transaction (scope narrowed to the property, then restored).
func (b *BI) SeedRefresh(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var all, ids string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('app.all_properties', true), ''), coalesce(current_setting('app.property_ids', true), '')`).
		Scan(&all, &ids); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.all_properties', 'off', true), set_config('app.property_ids', $1, true)`,
		"{"+property.String()+"}"); err != nil {
		return err
	}
	today := b.now().In(calendar.Location(ctx, tx))
	from := monthStart(today)
	if _, err := b.refreshProperty(ctx, tx, property, "seed", from, today, nil); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT set_config('app.all_properties', $1, true), set_config('app.property_ids', $2, true)`, all, ids)
	return err
}

// ── jobs ──────────────────────────────────────────────────────────────────

// AnalyticsRefreshArgs runs a refresh of every property.
type AnalyticsRefreshArgs struct {
	Mode string `json:"mode"`
}

func (AnalyticsRefreshArgs) Kind() string { return "analytics_refresh" }

func (AnalyticsRefreshArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// AnalyticsRefreshWorker runs the refresh.
type AnalyticsRefreshWorker struct {
	river.WorkerDefaults[AnalyticsRefreshArgs]
	BI *BI
}

func (w *AnalyticsRefreshWorker) Timeout(*river.Job[AnalyticsRefreshArgs]) time.Duration {
	return 30 * time.Minute
}

func (w *AnalyticsRefreshWorker) Work(ctx context.Context, job *river.Job[AnalyticsRefreshArgs]) error {
	kind := job.Args.Mode
	if kind != "backfill" {
		kind = "incremental"
	}
	_, err := w.BI.Refresh(ctx, kind)
	return err
}

// RegisterJobs adds the analytics refresh (every 10 minutes, nightly
// backfill) and the scheduled report dispatcher (every 5 minutes).
func (b *BI) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &AnalyticsRefreshWorker{BI: b})
	river.AddWorker(reg.Workers, &ScheduledReportWorker{BI: b})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(river.PeriodicInterval(10*time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return AnalyticsRefreshArgs{Mode: "incremental"}, nil
		}, nil),
		river.NewPeriodicJob(jobs.DailyAt{Hour: 2, Minute: 15, Location: loc}, func() (river.JobArgs, *river.InsertOpts) {
			return AnalyticsRefreshArgs{Mode: "backfill"}, nil
		}, nil),
		river.NewPeriodicJob(river.PeriodicInterval(5*time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return ScheduledReportArgs{}, nil
		}, nil))
}

// ── HTTP ──────────────────────────────────────────────────────────────────

func (b *BI) refreshNow(ctx context.Context, tx pgx.Tx, r *http.Request, req AnalyticsRefreshRequest) (AnalyticsRefreshRun, error) {
	property := handle.Property(ctx)
	pol := LoadBIPolicy(ctx, tx, property)
	loc := calendar.Location(ctx, tx)
	today := day(b.now().In(loc))
	from, to := today.AddDate(0, 0, -(pol.RefreshWindowDays-1)), today
	if req.From != "" {
		t, err := time.Parse(dateFmt, req.From)
		if err != nil {
			return AnalyticsRefreshRun{}, handle.Invalid("from", "invalid", "from must be YYYY-MM-DD")
		}
		from = t
	}
	if req.To != "" {
		t, err := time.Parse(dateFmt, req.To)
		if err != nil {
			return AnalyticsRefreshRun{}, handle.Invalid("to", "invalid", "to must be YYYY-MM-DD")
		}
		to = t
	}
	if to.After(today) {
		to = today
	}
	if from.After(to) {
		return AnalyticsRefreshRun{}, handle.Invalid("from", "invalid", "from must be on or before to")
	}
	if int(to.Sub(from).Hours()/24)+1 > pol.BackfillDays {
		return AnalyticsRefreshRun{}, handle.Invalid("from", "too_long", fmt.Sprintf("refresh at most %d days at a time (BI Policies)", pol.BackfillDays))
	}
	uid := handle.UserID(ctx)
	run, err := b.refreshProperty(ctx, tx, property, "manual", from, to, &uid)
	if err != nil {
		return run, err
	}
	return run, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionCreate, EntityType: "analytics.refresh_run",
		EntityID: run.ID.String(), EntityLabel: "Analytics refresh " + run.WindowFrom + " – " + run.WindowTo, PropertyID: &property,
		After: map[string]any{"from": run.WindowFrom, "to": run.WindowTo, "kpis": run.KPIs, "values": run.ValuesWritten, "facts": run.FactsWritten}})
}

func (b *BI) status(ctx context.Context, tx pgx.Tx, r *http.Request) (AnalyticsStatus, error) {
	property := handle.Property(ctx)
	pol := LoadBIPolicy(ctx, tx, property)
	out := AnalyticsStatus{StaleAfterMinutes: pol.StaleAfterMinutes}
	today := day(b.now().In(calendar.Location(ctx, tx)))
	asOf, err := dataAsOf(ctx, tx, property, monthStart(today))
	if err != nil {
		return out, err
	}
	out.DataAsOf = asOf
	if asOf != nil {
		m := int(b.now().Sub(*asOf).Minutes())
		out.LatencyMinutes = &m
		out.Stale = m > pol.StaleAfterMinutes
	} else {
		out.Stale = true
	}
	runs, err := handle.List[AnalyticsRefreshRun](tx.Query(ctx, `SELECT id, property_id, kind, window_from::text AS window_from, window_to::text AS window_to,
		status, kpis, values_written, facts_written, duration_ms, error, started_at, finished_at FROM analytics.refresh_runs WHERE property_id = $1
		ORDER BY started_at DESC LIMIT 20`, property))
	out.Runs = runs
	return out, err
}

// dataAsOf is the oldest refresh time of the month values of a property.
func dataAsOf(ctx context.Context, q dbtx.Querier, property uuid.UUID, month time.Time) (*time.Time, error) {
	var t *time.Time
	err := q.QueryRow(ctx, `SELECT min(refreshed_at) FROM analytics.kpi_values WHERE property_id = $1 AND grain = 'month' AND period_start = $2::date`,
		property, month.Format(dateFmt)).Scan(&t)
	return t, err
}

func (b *BI) registerStore(add func(route.Route)) {
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/reporting/analytics:refresh", Scope: route.ScopeProperty, Permission: PermAnalyticsRefresh,
		Summary: "Refresh the analytics store of the property now (default: the incremental window of the BI Policies)",
		Request: AnalyticsRefreshRequest{}, Response: AnalyticsRefreshRun{}, Status: http.StatusOK,
		Handler: handle.Write(b.S.DB, http.StatusOK, b.refreshNow)})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/analytics/status", Scope: route.ScopeProperty, Permission: PermAnalyticsRefresh,
		Summary: "Freshness of the analytics store and the last refresh runs", Response: AnalyticsStatus{},
		Handler: handle.Read(b.S.DB, b.status)})
}
