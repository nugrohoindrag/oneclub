// Package corehr is Core HR of PRD P5: EP-01 Organization & Employee,
// EP-02 Employment Contract & Documents, EP-04 Training & Certification,
// the framework and core sections of EP-16 Employee Self Service, the HR
// part of EP-28 migration and the H6 / H7 contracts. Other P5 areas use the
// public API of the hris root package, not this package.
package corehr

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/storage"
)

// AccountRequest creates (or links) the login of an employee at onboarding
// (FR-HR-04).
type AccountRequest struct {
	EmployeeID uuid.UUID
	PropertyID uuid.UUID
	FullName   string
	Email      string
	Phone      *string
	Locale     *string
	RoleCodes  []string
	// ExistingUserID links an existing user instead of creating one.
	ExistingUserID *uuid.UUID
}

// Accounts provisions logins through IAM (wired by internal/app).
type Accounts interface {
	ProvisionEmployeeUser(ctx context.Context, tx pgx.Tx, r AccountRequest) (uuid.UUID, error)
}

// PartnerDirectory resolves partner caddies (golf) and instructors (sport
// club) of a property; wired by internal/app because hris does not import
// the business lines. A nil directory accepts any id.
type PartnerDirectory interface {
	// PartnerName returns the name of a caddy / instructor ("" = not found).
	PartnerName(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind string, id uuid.UUID) (string, error)
	// PartnerByCode finds a caddy / instructor by its code (nil = not found).
	PartnerByCode(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind, code string) (*uuid.UUID, string, error)
}

// Module is the Core HR service.
type Module struct {
	DB       *dbtx.DB
	Engine   *resource.Engine
	Events   hris.Publisher
	Notify   notify.Sender
	Files    *storage.Files
	Accounts Accounts
	Partners PartnerDirectory
	StaffURL func() string
	// Location is the instance timezone (jobs); requests use the property's.
	Location func() *time.Location
	// Logo returns the branding logo printed on HR letters (nil = name only).
	Logo func(ctx context.Context, q dbtx.Querier) []byte
}

// Register adds the Core HR routes, resources and ESS endpoints.
func (m *Module) Register(reg *route.Registry) {
	for _, d := range m.Defs() {
		m.Engine.Register(reg, d)
	}
	m.registerEmployees(reg)
	m.registerContracts(reg)
	m.registerDocuments(reg)
	m.registerCertifications(reg)
	m.registerTraining(reg)
	m.registerESS(reg)
	m.registerImports(reg)
	m.registerReconciliation(reg) // EP-28 FR-MIG-P5-05
	m.registerLetters(reg)
	m.registerDashboard(reg)
	m.registerLifecycleStatus(reg)
}

// add registers a property-scoped hris route.
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

// today is the calendar date of a property (UTC midnight).
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

// staffLink is a Staff App deep link.
func (m *Module) staffLink(path string) string {
	if m.StaffURL == nil {
		return path
	}
	return strings.TrimRight(m.StaffURL(), "/") + path
}

// notifyUsers sends an in-app + e-mail notification.
func (m *Module) notifyUsers(ctx context.Context, tx pgx.Tx, property uuid.UUID, users []uuid.UUID, event, link string, data map[string]any) error {
	if m.Notify == nil || len(users) == 0 {
		return nil
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "hris", UserIDs: dedupe(users), Link: m.staffLink(link), PropertyID: &property,
		Data: data})
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

// hrUsers are the users holding a permission at the property (HR team).
func hrUsers(ctx context.Context, q dbtx.Querier, property uuid.UUID, perm string) []uuid.UUID {
	us, err := notify.Holders(ctx, q, property, perm)
	if err != nil {
		return nil
	}
	return us
}

// yearlyNumber issues PREFIX-YYYY-NNNNN gap-free per property and year.
func yearlyNumber(ctx context.Context, tx pgx.Tx, property uuid.UUID, prefix string, year int) (string, error) {
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO hris.sequences (property_id, prefix, year, last_value) VALUES ($1, $2, $3, 1)
		ON CONFLICT (property_id, prefix, year) DO UPDATE SET last_value = hris.sequences.last_value + 1 RETURNING last_value`,
		property, prefix, year).Scan(&n)
	if err != nil {
		return "", err
	}
	return prefix + "-" + itoa(year) + "-" + pad(n, 5), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func pad(n, digits int) string {
	s := itoa(n)
	for len(s) < digits {
		s = "0" + s
	}
	return s
}

// getOne adapts handle.One to a query result: getOne[T]("what")(q.Query(…)).
func getOne[T any](what string) func(pgx.Rows, error) (T, error) {
	return func(rows pgx.Rows, err error) (T, error) { return handle.One[T](rows, err, what) }
}

// listRead serves a list use case in the standard list envelope.
func listRead[T any](db *dbtx.DB, fn func(ctx context.Context, tx pgx.Tx, r *http.Request) ([]T, error)) http.HandlerFunc {
	return handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[T], error) {
		return handle.Page(fn(ctx, tx, r))
	})
}

// listWrite runs a write use case answering a list in the list envelope.
func listWrite[Req any, T any](db *dbtx.DB, status int, fn func(ctx context.Context, tx pgx.Tx, r *http.Request, req Req) ([]T, error)) http.HandlerFunc {
	return handle.Write(db, status, func(ctx context.Context, tx pgx.Tx, r *http.Request, req Req) (httpx.Page[T], error) {
		return handle.Page(fn(ctx, tx, r, req))
	})
}
