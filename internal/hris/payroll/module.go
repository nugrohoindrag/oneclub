// Package payroll is the payroll of the HRIS (PRD P5 Mode A, §16 #1): EP-09
// Payroll Engine (pay components, salary structures per grade / position /
// employee, payroll runs with proration, overtime, unpaid leave, payroll
// inputs of other areas, adjustments, loans, THR, bonus, final settlement,
// retro corrections, year-to-date), EP-10 PPh 21 & BPJS (versioned
// statutory rate sets, e-Bupot / BPJS / 1721-A1 exports), EP-15 payroll
// accounting events, bank file, payment and payslips (PDF, Employee Self
// Service), the payroll reports of EP-27, the YTD and legacy payroll
// imports of EP-28 and the parallel run of EP-29.
//
// The package imports the hris root only: time & attendance through
// hris.TimeSummaries / ApprovedOvertime / LockTimePeriod, Core HR through the
// lookups, other areas through the payroll input registry; accounting is
// reached through the hris.payroll_posted / hris.payroll_paid events.
package payroll

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
	"oneclub/internal/platform/resource"
)

// Approvals is the approval engine (P0 EP-06).
type Approvals interface {
	Submit(ctx context.Context, tx pgx.Tx, s approval.SubmitRequest) (uuid.UUID, string, error)
	Cancel(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string) error
}

// Module is the payroll service.
type Module struct {
	DB        *dbtx.DB
	Engine    *resource.Engine
	Events    hris.Publisher
	Approvals Approvals
	Notify    notify.Sender
	StaffURL  func() string
	// Logo returns the branding logo printed on payslips (nil = name only).
	Logo func(ctx context.Context, q dbtx.Querier) []byte
}

// Register adds the routes and resources of payroll.
func (m *Module) Register(reg *route.Registry) {
	for _, d := range m.Defs() {
		m.Engine.Register(reg, d)
	}
	m.registerComponents(reg)
	m.registerStructures(reg)
	m.registerStatutory(reg)
	m.registerRuns(reg)
	m.registerAdjustments(reg)
	m.registerPayslips(reg)
	m.registerExports(reg)
	m.registerImports(reg)
	m.registerProfiles(reg)
}

// add registers a property-scoped hris route (ESS routes keep their scope).
func add(reg *route.Registry, tag string, rt route.Route) {
	rt.Module, rt.Tag = hris.Module, tag
	if rt.Scope == route.ScopeGlobal && !strings.HasPrefix(rt.Path, "/api/v1/ess") {
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

func mustDate(field, v string) (time.Time, error) {
	t, err := parseDate(field, v)
	if err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, handle.Invalid(field, "required", "is required (YYYY-MM-DD)")
	}
	return *t, nil
}

// parsePeriod parses a YYYY-MM period code.
func parsePeriod(field, v string) (time.Time, error) {
	t, err := time.Parse("2006-01", strings.TrimSpace(v))
	if err != nil {
		return time.Time{}, handle.Invalid(field, "invalid_period", "must be a period YYYY-MM")
	}
	return t, nil
}

func periodCode(t time.Time) string { return t.Format("2006-01") }

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

func dec(s string) decimal.Decimal { return hris.Dec(s) }

func money(d decimal.Decimal) string { return d.Round(2).String() }

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

// notifyUsers sends an in-app + e-mail notification.
func (m *Module) notifyUsers(ctx context.Context, tx pgx.Tx, property uuid.UUID, users []uuid.UUID, event, link string, data map[string]any,
	channels ...string) error {
	if m.Notify == nil || len(users) == 0 {
		return nil
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "hris", UserIDs: dedupe(users), Link: m.staffLink(link), PropertyID: &property,
		Data: data, Channels: channels})
}

func dedupe(in []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, u := range in {
		if u != uuid.Nil && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// holders are the users holding a permission at the property.
func holders(ctx context.Context, q dbtx.Querier, property uuid.UUID, perm string) []uuid.UUID {
	us, err := notify.Holders(ctx, q, property, perm)
	if err != nil {
		return nil
	}
	return us
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

// uuidParam parses an optional uuid query / filter parameter.
func uuidParam(r *http.Request, name string) (*uuid.UUID, error) {
	v := filterParam(r, name)
	if v == "" {
		return nil, nil
	}
	u, err := uuid.Parse(v)
	if err != nil {
		return nil, handle.Invalid(name, "invalid", "must be a uuid")
	}
	return &u, nil
}

// maskTail keeps the last 4 characters of an identity / account number.
func maskTail(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return strings.Repeat("*", len(r)-4) + string(r[len(r)-4:])
}

func maskPtr(s *string, show bool) *string {
	if s == nil || show {
		return s
	}
	v := maskTail(*s)
	return &v
}
