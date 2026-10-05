package app

// Trial dataset (`oneclub seed-demo --trial`): a club trial instance that
// looks alive. After the configuration seed (`oneclub seed-demo`), the trial
// step simulates the club for the last 90 days and books the next 30, so
// dashboards, reports, KPIs, the accounting books, CRM and the member app
// show meaningful, internally consistent numbers.
//
// How it works
//
//   - Everything goes through the modules' own use cases: the seeder calls
//     the in-process HTTP API (the same routes, validation, audit, outbox
//     events and approvals as the apps) as demo staff users, then dispatches
//     the outbox so journals, stock movements, loyalty points, CRM
//     interactions … are created by the normal subscribers. Raw SQL is used
//     only where no use case exists (demo users and their sessions, the
//     opening business day, the trial markers) — see trialRawSQL.
//   - Time: the history starts Days before the open business date of the
//     main property. The day before it is recorded as the opening (closed)
//     business day, so billing dates every charge and payment on the
//     simulated day; each day ends with the cashier shift close and the
//     Night Audit, which closes it and posts the daily revenue journal. The
//     application clock (kernel/clock) follows the simulated time of day,
//     and the outbox events of a simulated moment are dated at that moment
//     before they are dispatched, so event-driven postings (inventory,
//     procurement, loyalty …) carry the simulated date too. Timestamps the
//     database sets itself (DEFAULT now(): created_at, posted_at …) of the
//     tables the reports read by time are moved to the simulated moment
//     after every step (trialRedated); append-only tables (journals, stock
//     movements, ledgers, the audit log) keep the real seeding time next to
//     their simulated business dates. Background jobs the history needs run
//     at their simulated time from the seeders (loyalty night job, Top
//     Spender snapshot, campaign dispatch); stop the worker of the instance
//     while the trial step runs.
//   - Deterministic: every step draws from a random source derived from the
//     fixed seed and the step key, so two fresh instances get the same data.
//   - Idempotent: each completed step (setup of a seeder, a simulated day, a
//     final step) leaves a marker in the audit log (entity type
//     platform.trial_dataset); re-running skips completed steps and adds
//     nothing once the dataset is complete. A run interrupted in the middle
//     of a step repeats that step only.
//
// Seeders (trial_*.go, by Order): finance go-live 1 (book, periods, bank
// accounts, opening balances, mock-efaktur), day open 5, people 10
// (Rhapsody go-live migration, applications, renewals), golf 20, tournaments
// 25, POS & recipes 30, promotions & packages 35, sport club & stay 40,
// banquet 50, CRM sales 60, engagement & loyalty 70, purchasing &
// inventory 80, receivables & month-end close 85, day close 900 (cashier
// shift, Night Audit).
//
// Extension point (P5 areas and later releases): an area registers its
// seeder from an init() function in its own file; seeders run by Order
// within each step (setup, every day, final):
//
//	func init() {
//		registerTrialSeeder(trialSeeder{Name: "hr", Order: 600,
//			Setup: func(ctx context.Context, t *Trial) error { … },             // master data, once, before the history
//			Day:   func(ctx context.Context, t *Trial, day time.Time) error { … }, // every past business day, oldest first
//			Final: func(ctx context.Context, t *Trial) error { … },             // at the current date: upcoming business, closes
//		})
//	}
//
// Inside a step, t.As(email) (a demo user) or t.Admin() (the Property
// Admin) returns an API client; t.Rand(key) a deterministic random
// source; t.At(day, "HH:MM") moves the clock to that time of the simulated
// day (dispatching the events of the previous moment). Codes of trial
// records start with "TRL" so they are easy to recognise. The coverage
// summary (TrialCoverage) lists the counts per module; add the area's main
// tables to trialCoverageTables.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/iam/password"
)

// TrialDatabaseURL is the database URL of the process that seeds the trial
// dataset: its sessions commit asynchronously (synchronous_commit=off).
// The seeding is thousands of small transactions; waiting for every WAL
// flush would make it several times slower, and a crash loses at most the
// last moments of a run, which simply resumes (atomically ordered, the
// step markers go with their data).
func TrialDatabaseURL(url string) string {
	if url == "" || strings.Contains(url, "synchronous_commit=") {
		return url
	}
	if strings.Contains(url, "?") {
		return url + "&synchronous_commit=off"
	}
	if strings.HasPrefix(url, "postgres://") || strings.HasPrefix(url, "postgresql://") {
		return url + "?synchronous_commit=off"
	}
	return url + " synchronous_commit=off" // key=value connection string
}

// TrialOptions control the trial dataset.
type TrialOptions struct {
	Days  int    // simulated past business days (default 90)
	Ahead int    // days of upcoming business (default 30)
	Seed  uint64 // random seed (default 20260401)
	// Log receives progress lines (nil = slog).
	Log func(format string, args ...any)
}

// TrialResult reports a trial dataset run.
type TrialResult struct {
	AlreadyComplete bool          // nothing was added
	Property        uuid.UUID     // MAIN
	Start, Today    string        // first simulated business date, open business date
	Steps           int           // steps run now
	Duration        time.Duration // of this run
	Users           []DemoUser    // demo staff used by the dataset
	Coverage        []TrialCount
}

// trialSeeder is one business area of the trial dataset.
type trialSeeder struct {
	Name  string
	Order int
	Setup func(ctx context.Context, t *Trial) error
	Day   func(ctx context.Context, t *Trial, day time.Time) error
	Final func(ctx context.Context, t *Trial) error
}

// trialSeeders is the registry of the trial dataset (see the file header).
var trialSeeders []trialSeeder

// registerTrialSeeder adds a seeder; names are unique.
func registerTrialSeeder(s trialSeeder) {
	for _, x := range trialSeeders {
		if x.Name == s.Name {
			panic("trial seeder registered twice: " + s.Name)
		}
	}
	trialSeeders = append(trialSeeders, s)
}

func sortedTrialSeeders() []trialSeeder {
	out := append([]trialSeeder(nil), trialSeeders...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// TrialUsers are the demo staff of the areas that the configuration seed
// does not cover; they act and approve in the trial dataset (password and
// PIN as the other demo users).
var TrialUsers = []DemoUser{
	{"sales@demo.oneclub.id", "Doni Sales Executive", "sales_executive", "MAIN"},
	{"banquet.manager@demo.oneclub.id", "Ratih Banquet Manager", "banquet_manager", "MAIN"},
	{"banquet.sales@demo.oneclub.id", "Fikri Banquet Sales", "banquet_sales", "MAIN"},
	{"marketing@demo.oneclub.id", "Nadia Marketing Staff", "marketing_staff", "MAIN"},
	{"marketing.manager@demo.oneclub.id", "Kevin Marketing Manager", "marketing_manager", "MAIN"},
	{"crm.admin@demo.oneclub.id", "Gita CRM Admin", "crm_admin", "MAIN"},
	{"sport.manager@demo.oneclub.id", "Arif Sport Club Manager", "sport_club_manager", "MAIN"},
	{"sport.reception@demo.oneclub.id", "Mila Sport Club Receptionist", "sport_club_receptionist", "MAIN"},
	{"coach@demo.oneclub.id", "Coach Rizky", "instructor_coach", "MAIN"},
	{"outlet.manager@demo.oneclub.id", "Hadi Outlet Manager", "outlet_manager", "MAIN"},
	{"pos@demo.oneclub.id", "Tari POS Staff", "pos_staff", "MAIN"},
	{"pos.bar@demo.oneclub.id", "Rangga Bartender", "pos_staff", "MAIN"},
	{"pos.halfway@demo.oneclub.id", "Asih Halfway House", "pos_staff", "MAIN"},
	{"pos.sport@demo.oneclub.id", "Dina Sport Café", "pos_staff", "MAIN"},
	{"pos.proshop@demo.oneclub.id", "Yosef Pro Shop", "pos_staff", "MAIN"},
	{"resort.manager@demo.oneclub.id", "Bella Resort Manager", "resort_manager", "MAIN"},
	{"inventory@demo.oneclub.id", "Eko Inventory Manager", "inventory_manager", "MAIN"},
	{"warehouse@demo.oneclub.id", "Udin Warehouse Staff", "warehouse_staff", "MAIN"},
	{"procurement@demo.oneclub.id", "Sinta Procurement Staff", "procurement_staff", "MAIN"},
	{"procurement.manager@demo.oneclub.id", "Yoga Procurement Manager", "procurement_manager", "MAIN"},
	{"accountant@demo.oneclub.id", "Irma Accountant", "accountant", "MAIN"},
	{"club.manager@demo.oneclub.id", "Teddy Club Manager", "club_manager", "MAIN"},
}

// Trial is the state of a trial dataset run, handed to every seeder step.
type Trial struct {
	App      *App
	Property uuid.UUID      // MAIN
	Loc      *time.Location // instance timezone
	Start    time.Time      // first simulated business date (00:00 local)
	Today    time.Time      // the open business date after the history (00:00 local)
	Ahead    int

	seed     uint64
	h        http.Handler
	logf     func(format string, args ...any)
	marks    map[string]json.RawMessage
	tokens   map[string]string
	offset   time.Duration
	lastCut  time.Time
	segments []trialSegment // real-time windows to re-date (see redate)
	sessions []uuid.UUID
	steps    int
	refs     map[string]string // shared references between seeders (code → id)
	members  []trialMember
	guests   []trialGuest
	mu       sync.Mutex // guards the maps and statistics below (Parallel)
	// statistics of the current step
	calls             int
	callTime, flushes time.Duration
	profile           map[string]*trialProfile // ONECLUB_TRIAL_PROFILE=1: time per route
}

type trialProfile struct {
	n int
	d time.Duration
}

var trialUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// Profile returns the API time per route of the run, slowest first.
func (t *Trial) Profile() []string {
	type row struct {
		k string
		p *trialProfile
	}
	var rows []row
	for k, p := range t.profile {
		rows = append(rows, row{k, p})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].p.d > rows[j].p.d })
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%7dms %6d× %6.1fms  %s", r.p.d.Milliseconds(), r.p.n, float64(r.p.d.Microseconds())/1000/float64(r.p.n), r.k))
	}
	return out
}

// record adds a timing to the profile.
func (t *Trial) record(k string, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.profile[k]
	if p == nil {
		p = &trialProfile{}
		t.profile[k] = p
	}
	p.n++
	p.d += d
}

// trialFail aborts a step from inside the API helpers.
type trialFail struct{ err error }

// SeedTrial adds the trial dataset to an instance that has the demo
// configuration (SeedDemo). It refuses to run when ONECLUB_ENV is
// production (checked by the command) and when the main property already
// closed business days outside the trial (a club in use).
func (a *App) SeedTrial(ctx context.Context, o TrialOptions) (res *TrialResult, err error) {
	began := time.Now()
	if o.Days <= 0 {
		o.Days = 90
	}
	if o.Ahead <= 0 {
		o.Ahead = 30
	}
	if o.Seed == 0 {
		o.Seed = 20260401
	}
	logf := o.Log
	if logf == nil {
		logf = func(format string, args ...any) { slog.Info(fmt.Sprintf(format, args...)) }
	}
	ctx = dbtx.System(ctx)
	t := &Trial{App: a, Ahead: o.Ahead, seed: o.Seed, h: a.Server.Handler(), logf: logf, tokens: map[string]string{}, refs: map[string]string{}}
	if os.Getenv("ONECLUB_TRIAL_PROFILE") != "" {
		t.profile = map[string]*trialProfile{}
		defer func() {
			for _, l := range t.Profile() {
				logf("%s", l)
			}
		}()
	}
	defer func() {
		clock.SetOffsetForTest(0)
		if rerr := t.revokeSessions(ctx); rerr != nil && err == nil {
			err = rerr
		}
	}()
	if err := a.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id FROM platform.properties WHERE code = 'MAIN'`).Scan(&t.Property); err != nil {
			return fmt.Errorf("property MAIN not found; run oneclub seed-demo first: %w", err)
		}
		var demo bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.courses WHERE property_id = $1 AND code = $2)`, t.Property, DemoCourseCode).Scan(&demo); err != nil {
			return err
		}
		if !demo {
			return errors.New("the demo configuration is missing; run oneclub seed-demo first")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	t.Loc = a.Instance.Location()
	if t.marks, err = t.loadMarks(ctx); err != nil {
		return nil, err
	}
	res = &TrialResult{Users: append(append([]DemoUser{}, DemoUsers...), append(P1DemoUsers, TrialUsers...)...)}
	if err := t.anchor(ctx, o.Days); err != nil {
		return nil, err
	}
	res.Start, res.Today, res.Property = t.Start.Format(time.DateOnly), t.Today.Format(time.DateOnly), t.Property
	if _, ok := t.marks["complete"]; ok {
		res.AlreadyComplete = true
		res.Coverage, err = TrialCoverage(ctx, a.DB, t.Property)
		return res, err
	}
	if err := t.step(ctx, "users", func() error { return t.ensureUsers(ctx) }); err != nil {
		return res, err
	}
	seeders := sortedTrialSeeders()
	// Setup: master data at the opening of the history.
	for _, s := range seeders {
		if s.Setup == nil {
			continue
		}
		if err := t.step(ctx, "setup:"+s.Name, func() error {
			t.At(t.Start.AddDate(0, 0, -1), "09:00")
			return s.Setup(ctx, t)
		}); err != nil {
			return res, err
		}
	}
	// History: every business day from Start to the day before Today.
	for d := t.Start; d.Before(t.Today); d = d.AddDate(0, 0, 1) {
		day := d
		if err := t.step(ctx, "day:"+day.Format(time.DateOnly), func() error {
			t.At(day, "06:00")
			for _, s := range seeders {
				if s.Day == nil {
					continue
				}
				began := time.Now()
				if err := s.Day(ctx, t, day); err != nil {
					return fmt.Errorf("%s: %w", s.Name, err)
				}
				if t.profile != nil {
					t.check(t.flush(ctx)) // the seeder's events count as its time
					t.record("seeder "+s.Name, time.Since(began))
				}
			}
			return nil
		}); err != nil {
			return res, err
		}
	}
	// Final: today and the upcoming business, at the real time.
	for _, s := range seeders {
		if s.Final == nil {
			continue
		}
		if err := t.step(ctx, "final:"+s.Name, func() error {
			t.Now()
			return s.Final(ctx, t)
		}); err != nil {
			return res, err
		}
	}
	if err := t.step(ctx, "complete", func() error { return t.cancelHistoricDeliveries(ctx) }); err != nil {
		return res, err
	}
	res.Steps, res.Duration = t.steps, time.Since(began)
	res.Coverage, err = TrialCoverage(ctx, a.DB, t.Property)
	return res, err
}

// ── markers ───────────────────────────────────────────────────────────────

func (t *Trial) loadMarks(ctx context.Context) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	err := t.App.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT entity_id, coalesce(after, '{}'::jsonb) FROM audit.audit_log
			WHERE entity_type = 'platform.trial_dataset' AND action = 'trial_step'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var v json.RawMessage
			if err := rows.Scan(&k, &v); err != nil {
				return err
			}
			out[k] = v
		}
		return rows.Err()
	})
	return out, err
}

func (t *Trial) mark(ctx context.Context, key string, after any) error {
	raw, _ := json.Marshal(after)
	if after == nil {
		raw = []byte(`{}`)
	}
	err := t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO audit.audit_log (id, actor_type, actor_name, module, action, category, entity_type, entity_id, entity_label,
			after, property_id) VALUES ($1, 'system', 'oneclub seed-demo --trial', 'platform', 'trial_step', 'system', 'platform.trial_dataset', $2, $3, $4, $5)`,
			id.New(), key, "Trial dataset · "+key, raw, t.Property)
		return err
	})
	if err == nil {
		t.marks[key] = raw
	}
	return err
}

// step runs fn once (marker key), dispatches the outbox and marks it done.
func (t *Trial) step(ctx context.Context, key string, fn func() error) (err error) {
	if _, ok := t.marks[key]; ok {
		return nil
	}
	started := time.Now()
	defer func() {
		if r := recover(); r != nil {
			f, ok := r.(trialFail)
			if !ok {
				panic(r)
			}
			err = fmt.Errorf("trial step %s: %w", key, f.err)
		}
	}()
	if err := fn(); err != nil {
		return fmt.Errorf("trial step %s: %w", key, err)
	}
	if err := t.flush(ctx); err != nil {
		return fmt.Errorf("trial step %s: %w", key, err)
	}
	if err := t.redate(ctx); err != nil {
		return fmt.Errorf("trial step %s: %w", key, err)
	}
	t.steps++
	t.logf("trial dataset: %-24s %6dms  (%d API calls %dms, dispatch %dms)", key, time.Since(started).Milliseconds(), t.calls,
		t.callTime.Milliseconds(), t.flushes.Milliseconds())
	t.calls, t.callTime, t.flushes = 0, 0, 0
	return t.mark(ctx, key, nil)
}

// fail aborts the current step.
func (t *Trial) fail(format string, args ...any) {
	panic(trialFail{fmt.Errorf(format, args...)})
}

// check aborts the current step on err.
func (t *Trial) check(err error) {
	if err != nil {
		panic(trialFail{err})
	}
}

// ── time ──────────────────────────────────────────────────────────────────

// anchor fixes the simulated history (first run) or reads it back.
func (t *Trial) anchor(ctx context.Context, days int) error {
	if raw, ok := t.marks["anchor"]; ok {
		var a struct{ Start, Today string }
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		s, err1 := time.ParseInLocation(time.DateOnly, a.Start, t.Loc)
		d, err2 := time.ParseInLocation(time.DateOnly, a.Today, t.Loc)
		if err := errors.Join(err1, err2); err != nil {
			return err
		}
		t.Start, t.Today = s, d
		return nil
	}
	var today time.Time
	err := t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var closed int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM billing.business_days WHERE property_id = $1 AND status = 'closed'`, t.Property).Scan(&closed); err != nil {
			return err
		}
		if closed > 0 {
			return errors.New("the trial dataset needs a fresh instance: the main property already closed business days")
		}
		var bd time.Time
		if err := tx.QueryRow(ctx, `SELECT billing.current_business_date($1)`, t.Property).Scan(&bd); err != nil {
			return err
		}
		today = time.Date(bd.Year(), bd.Month(), bd.Day(), 0, 0, 0, 0, t.Loc)
		opening := today.AddDate(0, 0, -days-1)
		// trialRawSQL: the opening business day has no use case — it is the
		// go-live day before the history, closed without a night audit.
		_, err := tx.Exec(ctx, `INSERT INTO billing.business_days (property_id, business_date, status, closed_at, summary)
			VALUES ($1, $2, 'closed', now(), '{"note":"Trial dataset opening day"}'::jsonb)`, t.Property, opening.Format(time.DateOnly))
		return err
	})
	if err != nil {
		return err
	}
	t.Start, t.Today = today.AddDate(0, 0, -days), today
	return t.mark(ctx, "anchor", map[string]string{"start": t.Start.Format(time.DateOnly), "today": t.Today.Format(time.DateOnly)})
}

// Day returns the local date at 00:00 n days after Start.
func (t *Trial) Day(n int) time.Time { return t.Start.AddDate(0, 0, n) }

// Clock returns hh:mm of a local day.
func (t *Trial) Clock(day time.Time, hhmm string) time.Time {
	h, m := 0, 0
	if p := strings.SplitN(hhmm, ":", 2); len(p) == 2 {
		h, _ = strconv.Atoi(p[0])
		m, _ = strconv.Atoi(p[1])
	}
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, t.Loc)
}

// At moves the application clock to hh:mm of a simulated day; the events of
// the previous moment are dated and dispatched first.
func (t *Trial) At(day time.Time, hhmm string) {
	t.check(t.flush(context.Background()))
	at := t.Clock(day, hhmm)
	t.offset = time.Until(at)
	clock.SetOffsetForTest(t.offset)
}

// trialSegment is a window of real time during which the application
// clock was shifted by off.
type trialSegment struct {
	from, to time.Time
	off      time.Duration
}

// trialRedated are the record timestamps that the database sets itself
// (DEFAULT now(): the real time of the run) and that the reports read as
// "when it happened" (Top Spender spend, NPS and ticket trends, the sales
// funnel, booking lead times). After every step they are moved to the
// simulated moment they were written at, as the outbox events are; values
// the modules set from the application clock are already simulated and lie
// outside the real-time windows, so they are left alone. Append-only tables
// (journals, stock movements, ledgers, the audit log) keep their real
// timestamps: their business dates are simulated already.
var trialRedated = [][2]string{
	{"billing.folios", "created_at"}, {"billing.folio_lines", "posted_at"}, {"billing.folio_lines", "created_at"},
	{"billing.payments", "paid_at"}, {"billing.payments", "created_at"},
	{"crm.customers", "created_at"}, {"crm.interactions", "occurred_at"}, {"crm.interactions", "created_at"},
	{"crm.feedback_requests", "created_at"}, {"crm.feedback", "created_at"}, {"crm.nps_responses", "created_at"},
	{"crm.tickets", "created_at"}, {"crm.tickets", "first_responded_at"}, {"crm.tickets", "resolved_at"}, {"crm.tickets", "closed_at"},
	{"crm.ticket_events", "created_at"}, {"crm.sales_leads", "created_at"}, {"crm.sales_activities", "created_at"},
	{"crm.sales_opportunities", "created_at"}, {"crm.sales_quotations", "created_at"}, {"crm.sales_quotations", "sent_at"}, {"crm.campaigns", "created_at"},
	{"crm.campaign_recipients", "created_at"}, {"crm.loyalty_accounts", "created_at"},
	{"golf.bookings", "created_at"}, {"commercial.orders", "created_at"}, {"commercial.package_bookings", "created_at"},
	{"membership.applications", "created_at"}, {"banquet.events", "created_at"}, {"stay.stays", "created_at"},
	{"procurement.purchase_requisitions", "created_at"}, {"procurement.purchase_orders", "created_at"},
}

// redate moves the database-set timestamps written during the shifted
// windows of the step to their simulated moment (see trialRedated).
func (t *Trial) redate(ctx context.Context) error {
	if len(t.segments) == 0 {
		return nil
	}
	ctx = dbtx.System(ctx)
	froms, tos, secs := make([]time.Time, 0, len(t.segments)), make([]time.Time, 0, len(t.segments)), make([]float64, 0, len(t.segments))
	for _, s := range t.segments {
		froms, tos, secs = append(froms, s.from), append(tos, s.to), append(secs, s.off.Seconds())
	}
	err := t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
		for _, c := range trialRedated {
			if !t.columnExists(ctx, tx, c[0], c[1]) {
				continue
			}
			// trialRawSQL: table and column names come from the constant list above
			if _, err := tx.Exec(ctx, `UPDATE `+c[0]+` x SET `+c[1]+` = x.`+c[1]+` + make_interval(secs => s.secs) `+ //nolint:gosec // G202: constant list
				`FROM unnest($1::timestamptz[], $2::timestamptz[], $3::float8[]) AS s(f, t, secs) WHERE x.`+c[1]+` >= s.f AND x.`+c[1]+` < s.t`,
				froms, tos, secs); err != nil {
				return fmt.Errorf("re-date %s.%s: %w", c[0], c[1], err)
			}
		}
		return nil
	})
	t.segments = t.segments[:0]
	return err
}

// columnExists caches which re-dated columns the schema has.
func (t *Trial) columnExists(ctx context.Context, tx pgx.Tx, table, column string) bool {
	k := "column:" + table + "." + column
	if v := t.Ref(k); v != "" {
		return v == "1"
	}
	schema, name, _ := strings.Cut(table, ".")
	var ok bool
	t.check(tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = $3)`,
		schema, name, column).Scan(&ok))
	t.SetRef(k, map[bool]string{true: "1", false: "0"}[ok])
	return ok
}

// Now returns the clock to the real time (after the history).
func (t *Trial) Now() {
	t.check(t.flush(context.Background()))
	t.offset = 0
	clock.SetOffsetForTest(0)
}

// flush dates the pending outbox events at the simulated moment and
// dispatches them (subscribers may publish more; repeated until none).
func (t *Trial) flush(ctx context.Context) error {
	ctx = dbtx.System(ctx)
	began := time.Now()
	defer func() { t.flushes += time.Since(began) }()
	for range 50 {
		err := t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var cut time.Time
			if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&cut); err != nil {
				return err
			}
			if t.offset != 0 && cut.After(t.lastCut) {
				if n := len(t.segments); n > 0 && t.segments[n-1].to.Equal(t.lastCut) && t.segments[n-1].off == t.offset {
					t.segments[n-1].to = cut
				} else {
					t.segments = append(t.segments, trialSegment{from: t.lastCut, to: cut, off: t.offset})
				}
				if _, err := tx.Exec(ctx, `UPDATE platform.outbox SET occurred_at = occurred_at + make_interval(secs => $1)
					WHERE dispatched_at IS NULL AND occurred_at >= $2 AND occurred_at < $3`, t.offset.Seconds(), t.lastCut, cut); err != nil {
					return err
				}
			}
			t.lastCut = cut
			return nil
		})
		if err != nil {
			return err
		}
		// two dispatchers, like the outbox queue of the worker
		var wg sync.WaitGroup
		var ns [2]int
		var es [2]error
		for w := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ns[w], es[w] = t.App.Dispatcher.DispatchPending(ctx)
			}()
		}
		wg.Wait()
		if err := errors.Join(es[0], es[1]); err != nil {
			return err
		}
		n := ns[0] + ns[1]
		if n == 0 {
			return nil
		}
	}
	return errors.New("outbox: events keep coming after 50 dispatch rounds")
}

// Rand is a deterministic random source of a key (seed × key).
func (t *Trial) Rand(key string) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return rand.New(rand.NewPCG(t.seed, h.Sum64())) //nolint:gosec // G404: deterministic demo data, not security
}

// Ref returns a shared reference recorded by an earlier seeder ("" if none).
func (t *Trial) Ref(key string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refs[key]
}

// SetRef records a shared reference.
func (t *Trial) SetRef(key, value string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refs[key] = value
}

// ── users and sessions ────────────────────────────────────────────────────

// ensureUsers creates the trial demo staff (trialRawSQL: like seed-demo,
// demo users get a known password, which no use case allows).
func (t *Trial) ensureUsers(ctx context.Context) error {
	hash, err := password.Hash(DemoPassword)
	if err != nil {
		return err
	}
	pin, err := password.Hash(DemoPIN)
	if err != nil {
		return err
	}
	return t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
		for _, u := range TrialUsers {
			uid := id.New()
			if err := tx.QueryRow(ctx, `INSERT INTO platform.users (id, email, full_name, password_hash, password_changed_at, pin_hash, locale)
				VALUES ($1,$2,$3,$4,now(),$5,'id') ON CONFLICT (email) DO UPDATE SET full_name = EXCLUDED.full_name RETURNING id`,
				uid, u.Email, u.Name, hash, pin).Scan(&uid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO platform.role_assignments (id, user_id, role_id, property_id)
				SELECT $1, $2, r.id, p.id FROM platform.roles r, platform.properties p WHERE r.code = $3 AND p.code = $4
				AND NOT EXISTS (SELECT 1 FROM platform.role_assignments x WHERE x.user_id = $2 AND x.role_id = r.id AND x.property_id = p.id)`,
				id.New(), uid, u.Role, u.Property); err != nil {
				return err
			}
		}
		return nil
	})
}

// As returns an API client acting as a demo user.
func (t *Trial) As(email string) *TrialClient {
	t.mu.Lock()
	defer t.mu.Unlock()
	tok, ok := t.tokens[email]
	if !ok {
		tok = "ocs_" + secret.RandomToken(32)
		sid := id.New()
		ctx := dbtx.System(context.Background())
		t.check(t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
			// trialRawSQL: a server-side session for the seeder (no login,
			// MFA already verified); revoked when the run ends.
			tag, err := tx.Exec(ctx, `INSERT INTO platform.sessions (id, user_id, token_hash, kind, mfa_verified, user_agent, expires_at)
				SELECT $1, u.id, $2, 'web', true, 'oneclub seed-demo --trial', now() + interval '12 hours' FROM platform.users u WHERE u.email = $3`,
				sid, secret.HashToken(tok), email)
			if err == nil && tag.RowsAffected() == 0 {
				err = fmt.Errorf("demo user %s not found", email)
			}
			return err
		}))
		t.tokens[email] = tok
		t.sessions = append(t.sessions, sid)
	}
	return &TrialClient{t: t, Email: email, token: tok, Property: t.Property}
}

// Admin returns an API client acting as the Property Admin of the main
// property (club configuration).
func (t *Trial) Admin() *TrialClient { return t.As("property.admin@demo.oneclub.id") }

// Public is a client without a session (website, public links).
func (t *Trial) Public() *TrialClient { return &TrialClient{t: t} }

func (t *Trial) revokeSessions(ctx context.Context) error {
	if len(t.sessions) == 0 {
		return nil
	}
	return t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now(), revoked_reason = 'trial dataset finished' WHERE id = ANY($1)`, t.sessions)
		return err
	})
}

// cancelHistoricDeliveries stops e-mail / WhatsApp messages of the
// simulated history from being sent to the (fictitious) customers when a
// worker starts: they are marked skipped and their jobs cancelled.
func (t *Trial) cancelHistoricDeliveries(ctx context.Context) error {
	return t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE platform.notification_deliveries SET status = 'skipped', last_error = 'trial dataset: simulated history, not sent'
			WHERE status = 'pending' RETURNING job_id`)
		if err != nil {
			return err
		}
		jobs, err := pgx.CollectRows(rows, pgx.RowTo[*int64])
		if err != nil {
			return err
		}
		for _, j := range jobs {
			if j == nil || t.App.Jobs == nil {
				continue
			}
			if _, err := t.App.Jobs.River.JobCancelTx(ctx, tx, *j); err != nil {
				return err
			}
		}
		return nil
	})
}

// ── API client ────────────────────────────────────────────────────────────

// J is a JSON object of the API.
type J map[string]any

// S returns a field as a string ("" when missing).
func (j J) S(k string) string { return jstr(j[k]) }

// M returns a nested object.
func (j J) M(k string) J {
	m, _ := j[k].(map[string]any)
	return J(m)
}

// A returns a nested array of objects.
func (j J) A(k string) []J { return jarr(j[k]) }

// N returns a numeric field.
func (j J) N(k string) float64 {
	switch v := j[k].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// Items returns body.items.
func (j J) Items() []J { return j.A("items") }

func jstr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// jstrs reads an array of strings.
func jstrs(v any) []string {
	a, _ := v.([]any)
	out := make([]string, 0, len(a))
	for _, x := range a {
		out = append(out, jstr(x))
	}
	return out
}

func jarr(v any) []J {
	a, _ := v.([]any)
	out := make([]J, 0, len(a))
	for _, x := range a {
		if m, ok := x.(map[string]any); ok {
			out = append(out, J(m))
		}
	}
	return out
}

// TrialClient calls the in-process API as one user.
type TrialClient struct {
	t        *Trial
	Email    string
	token    string
	Property uuid.UUID
}

type trialRecorder struct {
	hdr    http.Header
	status int
	body   bytes.Buffer
}

func (r *trialRecorder) Header() http.Header { return r.hdr }
func (r *trialRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}
func (r *trialRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}

// Call performs a request and returns the status and the decoded body.
func (c *TrialClient) Call(method, path string, body any, hdr ...string) (int, J, []byte) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		c.t.check(err)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, "http://trial.oneclub.local"+path, rd)
	c.t.check(err)
	if req.Body == nil {
		req.Body = http.NoBody
	}
	req.RemoteAddr = "127.0.0.1:1"
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("User-Agent", "oneclub seed-demo --trial")
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Property != uuid.Nil {
		req.Header.Set("X-Property-Id", c.Property.String())
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := &trialRecorder{hdr: http.Header{}}
	began := time.Now()
	c.t.h.ServeHTTP(rec, req)
	c.t.mu.Lock()
	defer c.t.mu.Unlock()
	c.t.calls++
	c.t.callTime += time.Since(began)
	if c.t.profile != nil {
		k := method + " " + trialUUID.ReplaceAllString(strings.SplitN(path, "?", 2)[0], "{id}")
		p := c.t.profile[k]
		if p == nil {
			p = &trialProfile{}
			c.t.profile[k] = p
		}
		p.n++
		p.d += time.Since(began)
	}
	var out J
	raw := rec.body.Bytes()
	if len(raw) > 0 && raw[0] == '{' {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = J{}
	}
	return rec.status, out, raw
}

// Do performs a request that must succeed (2xx), else the step fails.
func (c *TrialClient) Do(method, path string, body any, hdr ...string) J {
	st, out, raw := c.Call(method, path, body, hdr...)
	if st < 200 || st > 299 {
		msg := string(raw)
		if len(msg) > 600 {
			msg = msg[:600] + "…"
		}
		c.t.fail("%s %s as %s: %d %s", method, path, c.Email, st, msg)
	}
	return out
}

// Post creates / acts (must succeed).
func (c *TrialClient) Post(path string, body any) J { return c.Do(http.MethodPost, path, body) }

// Get reads (must succeed).
func (c *TrialClient) Get(path string) J { return c.Do(http.MethodGet, path, nil) }

// Patch updates (must succeed).
func (c *TrialClient) Patch(path string, body any) J { return c.Do(http.MethodPatch, path, body) }

// Put replaces (must succeed).
func (c *TrialClient) Put(path string, body any) J { return c.Do(http.MethodPut, path, body) }

// Items lists (must succeed).
func (c *TrialClient) Items(path string) []J { return c.Get(path).Items() }

// Try performs a request that may fail; it reports whether it succeeded.
func (c *TrialClient) Try(method, path string, body any) (J, bool) {
	st, out, _ := c.Call(method, path, body)
	return out, st >= 200 && st <= 299
}

// Parallel runs fn(0 … n-1) on up to workers goroutines (independent API
// work of one simulated moment: the clock does not move inside). Draw the
// random choices before, so the data does not depend on the scheduling; a
// failure aborts the step once every worker stopped.
func (t *Trial) Parallel(n, workers int, fn func(i int)) {
	if n <= 0 {
		return
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	next := make(chan int)
	for range min(workers, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				func() {
					defer func() {
						if r := recover(); r != nil {
							err := fmt.Errorf("panic: %v", r)
							if f, ok := r.(trialFail); ok {
								err = f.err
							}
							mu.Lock()
							if first == nil {
								first = err
							}
							mu.Unlock()
						}
					}()
					mu.Lock()
					failed := first != nil
					mu.Unlock()
					if !failed {
						fn(i)
					}
				}()
			}
		}()
	}
	for i := range n {
		next <- i
	}
	close(next)
	wg.Wait()
	if first != nil {
		panic(trialFail{first})
	}
}

// ApprovePending approves every pending approval request of the property
// as an eligible demo user (the configured approval workflows of the demo:
// membership manager, finance manager, procurement manager …).
func (t *Trial) ApprovePending(ctx context.Context) {
	for range 5 {
		type req struct {
			ID    uuid.UUID
			Email string
		}
		var reqs []req
		t.check(t.App.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT r.id, CASE WHEN s.approver_type = 'user' THEN (SELECT email FROM platform.users WHERE id = s.approver_user_id)
					ELSE (SELECT u.email FROM platform.users u JOIN platform.role_assignments ra ON ra.user_id = u.id
					WHERE ra.role_id = s.approver_role_id AND u.status = 'active' AND u.id <> r.requested_by AND (ra.property_id IS NULL OR ra.property_id = r.property_id)
					ORDER BY (u.email LIKE '%@demo.oneclub.id') DESC, u.email LIMIT 1) END
				FROM platform.approval_requests r JOIN platform.approval_request_steps s ON s.request_id = r.id AND s.step_no = r.current_step_no
				WHERE r.status = 'pending' AND r.property_id = $1 ORDER BY r.created_at`, t.Property)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var r req
				var email *string
				if err := rows.Scan(&r.ID, &email); err != nil {
					return err
				}
				if email != nil {
					r.Email = *email
					reqs = append(reqs, r)
				}
			}
			return rows.Err()
		}))
		if len(reqs) == 0 {
			return
		}
		for _, r := range reqs {
			t.As(r.Email).Post("/api/v1/platform/approvals/"+r.ID.String()+":approve", J{"reason": "Approved (trial dataset)"})
		}
		t.check(t.flush(ctx))
	}
}
