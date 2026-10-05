// Package talent is Recruitment (PRD P5 EP-03) and Performance Review
// (EP-05) of the HRIS: job requisitions with approval, candidates,
// applications through the pipeline, interviews and scorecards, offers with
// approval and offer letter, the hire through Core HR, the public careers
// page, applicant data retention, review templates and cycles, self and
// manager reviews in Employee Self Service, HR calibration, results to the
// employment history and promotion, and their HR reports (EP-27).
//
// The package imports the hris root only (Core HR is reached through
// hris.Onboarding, wired by internal/app).
package talent

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
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/storage"
)

// Module is the recruitment & performance review service.
type Module struct {
	DB         *dbtx.DB
	Engine     *resource.Engine
	Events     hris.Publisher
	Notify     notify.Sender
	Approvals  *approval.Engine
	Files      *storage.Files
	Onboarding hris.Onboarding
	StaffURL   func() string
	WebsiteURL func() string
	// Logo returns the branding logo printed on offer letters (nil = name only).
	Logo func(ctx context.Context, q dbtx.Querier) []byte
}

// Register adds the routes and resources.
func (m *Module) Register(reg *route.Registry) {
	for _, d := range m.Defs() {
		m.Engine.Register(reg, d)
	}
	m.registerRequisitions(reg)
	m.registerApplications(reg)
	m.registerInterviews(reg)
	m.registerOffers(reg)
	m.registerCareers(reg)
	m.registerCycles(reg)
	m.registerReviews(reg)
	m.registerESS(reg)
}

// add registers a property-scoped hris route (ESS and public routes keep
// their scope).
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
	return prefix + "-" + itoa(year) + "-" + pad(n, 5), nil
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

// notifyCandidate e-mails a candidate (no user account) when the Recruitment
// Configuration allows it.
func (m *Module) notifyCandidate(ctx context.Context, tx pgx.Tx, property uuid.UUID, email *string, name, event string, data map[string]any) error {
	if m.Notify == nil || email == nil || *email == "" {
		return nil
	}
	cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, property, clock.Now())
	if err != nil || !cfg.NotifyCandidates {
		return err
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "hris", Email: *email, Name: name, Channels: []string{"email"},
		PropertyID: &property, Data: data, Mandatory: true})
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

// userOf returns the active login of an employee (nil when none).
func userOf(ctx context.Context, q dbtx.Querier, employee *uuid.UUID) *uuid.UUID {
	if employee == nil {
		return nil
	}
	var u *uuid.UUID
	_ = q.QueryRow(ctx, `SELECT id FROM platform.users WHERE employee_id = $1 AND status = 'active'`, *employee).Scan(&u)
	return u
}

// getOne adapts handle.One to a query result.
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

// likeParam is the ILIKE pattern of ?q= ("" = no search).
func likeParam(r *http.Request) string {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		return ""
	}
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
}
