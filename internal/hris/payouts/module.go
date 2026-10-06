// Package payouts is Payouts & distributions of the HRIS (PRD P5): the
// service charge distribution of the month's pool (EP-11, §16 #5), sales
// commission and bonus payout through payroll (EP-12), and the payout runs
// of the non-employee workforce — caddies (EP-13) and partner instructors
// (EP-14) — with PPh 21 non-employee, BPJS BPU, statements and the bank
// file; their reports (EP-27) and Employee Self Service / caddy / instructor
// statements (EP-16, EP-26).
//
// The package imports the hris root (and the accounting root for the service
// charge pool, contract H4). golf, sport club and CRM are reached through
// their outbox events (H1–H3) and the Directory internal/app plugs in; the
// payroll engine pays service charge, commission, bonus and employee
// instructor fees through the payroll input registry and never the reverse.
package payouts

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Source types of payout sources (contracts H1, H2) and commission
// statements (H3).
const (
	SourceCaddySettlement     = "golf.caddy_settlement"
	SourceInstructorFee       = "sportclub.instructor_fee"
	SourceCommissionStatement = "crm.commission_statement"
)

// Partner is a caddy (golf) or instructor (sport club) as the payouts see it.
type Partner struct {
	ID          uuid.UUID
	Kind        string
	Code        string
	Name        string
	UserID      *uuid.UUID // Caddy Tablet / instructor login
	EmployeeID  *uuid.UUID // instructor employed by the club (paid by payroll)
	Partnership string     // partner | employee
}

// CommissionBreakdown is the content of an approved commission statement.
type CommissionBreakdown struct {
	Earned, Clawback, Adjustments decimal.Decimal
	UserName                      string
}

// Directory is what the payouts read from and tell the business lines,
// wired by internal/app (hris does not import golf, sportclub or crm).
type Directory interface {
	// Partner returns a caddy or an instructor (nil when unknown).
	Partner(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind string, id uuid.UUID) (*Partner, error)
	// PartnerOfUser returns the caddy / instructor whose login is the user.
	PartnerOfUser(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind string, user uuid.UUID) (*Partner, error)
	// Commission returns the earned, clawback and adjustment totals of a
	// commission statement (ok false when unknown).
	Commission(ctx context.Context, q dbtx.Querier, statementID uuid.UUID) (CommissionBreakdown, bool, error)
	// Units returns the sessions of an instructor fee (statement detail).
	Units(ctx context.Context, q dbtx.Querier, sourceType string, id uuid.UUID) (int, error)
	// PaidElsewhere returns the sources already paid by their own module
	// (golf settlement / sport club fee payout), which a run must skip.
	PaidElsewhere(ctx context.Context, q dbtx.Querier, sourceType string, ids []uuid.UUID) (map[uuid.UUID]bool, error)
	// MarkPaid tells the owner module that hris paid the sources (caddy
	// settlements, instructor fees, commission statements → Paid).
	MarkPaid(ctx context.Context, q dbtx.Querier, property uuid.UUID, sourceType string, ids []uuid.UUID, reference string) error
}

// Module is the payouts service.
type Module struct {
	DB        *dbtx.DB
	Engine    *resource.Engine
	Events    hris.Publisher
	Approvals *approval.Engine
	Notify    notify.Sender
	Directory Directory
	StaffURL  func() string
}

// Register adds the routes and resources.
func (m *Module) Register(reg *route.Registry) {
	for _, d := range m.Defs() {
		m.Engine.Register(reg, d)
	}
	m.registerServiceCharge(reg)
	m.registerRuns(reg)
	m.registerCommissions(reg)
	m.registerBonuses(reg)
	m.registerStatements(reg)
	m.registerESS(reg)
}

// RegisterDecisions registers the approval document types with their
// decision hooks.
func (m *Module) RegisterDecisions(register func(dt provision.DocumentType, hook approval.DecisionHook)) {
	register(DistributionDocumentType, m.DistributionDecision)
	register(PayoutRunDocumentType, m.RunDecision)
	register(BonusDocumentType, m.BonusDecision)
}

// add registers a property-scoped hris route (ESS keeps its scope).
func add(reg *route.Registry, tag string, rt route.Route) {
	rt.Module, rt.Tag = hris.Module, tag
	if rt.Scope == route.ScopeGlobal && rt.Auth == route.AuthRequired && !strings.HasPrefix(rt.Path, "/api/v1/ess") {
		rt.Scope = route.ScopeProperty
	}
	reg.Add(rt)
}

// ── helpers ───────────────────────────────────────────────────────────────

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		u := p.UserID
		return &u
	}
	return nil
}

func can(ctx context.Context, perm string, property uuid.UUID) bool {
	p := authz.From(ctx)
	return p != nil && p.Can(perm, &property)
}

func location(ctx context.Context, q dbtx.Querier, property uuid.UUID) *time.Location {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}

// today is the club's business date (property time zone) as UTC midnight.
func today(ctx context.Context, q dbtx.Querier, property uuid.UUID) time.Time {
	l := clock.Now().In(location(ctx, q, property))
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

func ymd(t time.Time) string { return t.Format("2006-01-02") }

func parseDate(field, v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, handle.Invalid(field, "invalid_date", "must be a date (YYYY-MM-DD)")
	}
	return &t, nil
}

func nullStr(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func dec(s string) decimal.Decimal { return hris.Dec(s) }

func oneOf(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func enumErr(field string, list []string) error {
	return handle.Invalid(field, "invalid", "one of: "+strings.Join(list, ", "))
}

func getOne[T any](what string) func(pgx.Rows, error) (T, error) {
	return func(rows pgx.Rows, err error) (T, error) { return handle.One[T](rows, err, what) }
}

// listRead serves a list use case in the standard list envelope.
func listRead[T any](db *dbtx.DB, fn func(ctx context.Context, tx pgx.Tx, r *http.Request) ([]T, error)) http.HandlerFunc {
	return handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[T], error) {
		return handle.Page(fn(ctx, tx, r))
	})
}

// filterParam reads ?<name>= or ?filter[<name>]= (list pages).
func filterParam(r *http.Request, name string) string {
	q := r.URL.Query()
	if v := strings.TrimSpace(q.Get(name)); v != "" {
		return v
	}
	return strings.TrimSpace(q.Get("filter[" + name + "]"))
}

func intParam(r *http.Request, name string) int {
	n, _ := strconv.Atoi(filterParam(r, name))
	return n
}

// yearlyNumber issues PREFIX-YYYY-NNNNN gap-free per property and year
// (hris.sequences, shared with Core HR).
func yearlyNumber(ctx context.Context, tx pgx.Tx, property uuid.UUID, prefix string, year int) (string, error) {
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO hris.sequences (property_id, prefix, year, last_value) VALUES ($1, $2, $3, 1)
		ON CONFLICT (property_id, prefix, year) DO UPDATE SET last_value = hris.sequences.last_value + 1 RETURNING last_value`,
		property, prefix, year).Scan(&n)
	if err != nil {
		return "", err
	}
	s := strconv.Itoa(n)
	for len(s) < 5 {
		s = "0" + s
	}
	return prefix + "-" + strconv.Itoa(year) + "-" + s, nil
}

// staffLink is a Staff App deep link.
func (m *Module) staffLink(path string) string {
	if m.StaffURL == nil {
		return path
	}
	return strings.TrimRight(m.StaffURL(), "/") + path
}

func (m *Module) notifyUsers(ctx context.Context, tx pgx.Tx, property uuid.UUID, users []uuid.UUID, event, link string, data map[string]any) error {
	if m.Notify == nil || len(users) == 0 {
		return nil
	}
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, u := range users {
		if u != uuid.Nil && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "hris", UserIDs: out, Link: m.staffLink(link), PropertyID: &property, Data: data})
}

func holders(ctx context.Context, q dbtx.Querier, property uuid.UUID, perm string) []uuid.UUID {
	us, err := notify.Holders(ctx, q, property, perm)
	if err != nil {
		return nil
	}
	return us
}

// money formats an amount for notifications (Rp 1.234.567).
func money(v decimal.Decimal) string {
	s := v.Round(0).String()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b = append(b, '.')
		}
		b = append(b, c)
	}
	if neg {
		return "-Rp " + string(b)
	}
	return "Rp " + string(b)
}

// me is the employee of the signed-in user.
func me(ctx context.Context, tx pgx.Tx) (hris.Employee, error) {
	uid := handle.UserID(ctx)
	e, err := hris.EmployeeByUser(ctx, tx, uid)
	if err != nil {
		return hris.Employee{}, err
	}
	if e == nil {
		return hris.Employee{}, hris.ErrNotEmployee
	}
	return *e, nil
}
