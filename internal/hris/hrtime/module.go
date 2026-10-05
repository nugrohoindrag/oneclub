// Package hrtime is time & attendance of PRD P5: EP-06 Shift Scheduling,
// EP-07 Attendance (ESS mobile GPS with geofence, Attendance Kiosk with QR /
// PIN, biometric devices through the bridge agent, corrections, offline
// sync), EP-08 Leave, Permission & Overtime (balances with accrual and
// carry-over, requests with approval, overtime on the PP 35/2021 basis,
// holidays), their Employee Self Service sections (EP-16) and the leave
// balance import of EP-28. Other P5 areas use the hris root package
// (events, TimeSummaries, period locks), never this package.
package hrtime

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
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/storage"
)

// Approvals is the approval engine (P0 EP-06).
type Approvals interface {
	Submit(ctx context.Context, tx pgx.Tx, s approval.SubmitRequest) (uuid.UUID, string, error)
	Cancel(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string) error
}

// StaffingDemandDay is the operational demand of a date shown while scheduling
// (FR-SCH-04): tee times, events / BEO, sport club bookings.
type StaffingDemandDay struct {
	Date   string         `json:"date"`
	Values map[string]int `json:"values" doc:"Indicator → count, e.g. teeTimes, golfers, events, eventPax, classSessions, facilityBookings"`
}

// DemandSource reads the operational demand of a period (wired by
// internal/app: hris does not import the business lines).
type DemandSource func(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]StaffingDemandDay, error)

// Module is the time & attendance service.
type Module struct {
	DB        *dbtx.DB
	Engine    *resource.Engine
	Events    hris.Publisher
	Approvals Approvals
	Notify    notify.Sender
	Files     *storage.Files
	Demand    DemandSource
	StaffURL  func() string
	// Location is the instance timezone (jobs); requests use the property's.
	Location func() *time.Location
	// Secret signs the personal attendance QR codes of ESS.
	Secret []byte
	// Partners resolves caddies and instructors for device clock-in
	// (FR-ATT-08; nil = partners cannot be enrolled).
	Partners PartnerDirectory
}

// Register adds the routes and resources of time & attendance.
func (m *Module) Register(reg *route.Registry) {
	for _, d := range m.Defs() {
		m.Engine.Register(reg, d)
	}
	m.registerSchedules(reg)
	m.registerSwaps(reg)
	m.registerAttendance(reg)
	m.registerDevices(reg)
	m.registerPartners(reg)
	m.registerCorrections(reg)
	m.registerLeave(reg)
	m.registerPermissions(reg)
	m.registerOvertime(reg)
	m.registerLocks(reg)
	m.registerESS(reg)
	hris.SetAttendanceFinalizer(m.FinalizePeriod)
}

// add registers a property-scoped hris route.
func add(reg *route.Registry, tag string, rt route.Route) {
	rt.Module, rt.Tag = hris.Module, tag
	if rt.Scope == route.ScopeGlobal && !strings.HasPrefix(rt.Path, "/api/v1/ess") && !strings.HasPrefix(rt.Path, "/api/v1/bridge") {
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

// today is the business date of a property (UTC midnight).
func today(ctx context.Context, q dbtx.Querier, property uuid.UUID) time.Time {
	return dateOf(clock.Now(), location(ctx, q, property))
}

// dateOf is the local date of an instant (UTC midnight).
func dateOf(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// localAt is the instant of a local date and clock time.
func localAt(day time.Time, hh, mm int, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, loc)
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

// parseClock parses "HH:MM".
func parseClock(field, v string) (int, int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(v))
	if err != nil {
		return 0, 0, handle.Invalid(field, "invalid_time", "must be a time (HH:MM)")
	}
	return t.Hour(), t.Minute(), nil
}

// parseInstant parses an RFC 3339 instant.
func parseInstant(field, v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
	if err != nil {
		return time.Time{}, handle.Invalid(field, "invalid_datetime", "must be an RFC 3339 date-time")
	}
	return t.UTC(), nil
}

func nullStr(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return &s
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

// notifyUsers sends an in-app + e-mail (+ WhatsApp when asked) notification.
func (m *Module) notifyUsers(ctx context.Context, tx pgx.Tx, property uuid.UUID, users []uuid.UUID, event, link string, data map[string]any,
	channels ...string) error {
	users = dedupe(users)
	if m.Notify == nil || len(users) == 0 {
		return nil
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "hris", UserIDs: users, Link: m.staffLink(link), PropertyID: &property,
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

// userOf returns the active login of an employee (nil when none).
func userOf(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID) *uuid.UUID {
	var u uuid.UUID
	if err := q.QueryRow(ctx, `SELECT id FROM platform.users WHERE employee_id = $1 AND status = 'active'`, employeeID).Scan(&u); err != nil {
		return nil
	}
	return &u
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
	s := "00000" + itoa(n)
	return prefix + "-" + itoa(year) + "-" + s[len(s)-5:], nil
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

// getOne adapts handle.One to a query result.
func getOne[T any](what string) func(pgx.Rows, error) (T, error) {
	return func(rows pgx.Rows, err error) (T, error) { return handle.One[T](rows, err, what) }
}

// me returns the employee of the signed-in user (ESS).
func me(ctx context.Context, q dbtx.Querier) (hris.Employee, error) {
	uid := handle.UserID(ctx)
	if uid == uuid.Nil {
		return hris.Employee{}, errs.Unauthorized("sign in with your personal account")
	}
	e, err := hris.EmployeeByUser(ctx, q, uid)
	if err != nil {
		return hris.Employee{}, err
	}
	if e == nil {
		return hris.Employee{}, errs.NotFound("employee profile linked to your account")
	}
	return *e, nil
}

// meOptional returns the employee of the signed-in user or nil.
func meOptional(ctx context.Context, q dbtx.Querier) *hris.Employee {
	uid := handle.UserID(ctx)
	if uid == uuid.Nil {
		return nil
	}
	e, err := hris.EmployeeByUser(ctx, q, uid)
	if err != nil {
		return nil
	}
	return e
}

// employeeAt loads an employee of the property (404 otherwise).
func employeeAt(ctx context.Context, q dbtx.Querier, property, id uuid.UUID) (hris.Employee, error) {
	e, err := hris.EmployeeByID(ctx, q, id)
	if err != nil {
		return e, err
	}
	if e.PropertyID != property {
		return e, errs.NotFound("employee")
	}
	return e, nil
}

// checkLocked refuses a change of a day in a locked payroll period.
func checkLocked(ctx context.Context, q dbtx.Querier, property uuid.UUID, days ...time.Time) error {
	for _, d := range days {
		locked, ref, err := hris.TimeLocked(ctx, q, property, d)
		if err != nil {
			return err
		}
		if locked {
			return hris.ErrTimeLocked(d, ref)
		}
	}
	return nil
}

// statusErr is the conflict for a request in another status.
func statusErr(what, status string) error {
	return errs.Conflict("invalid_status", what+" is "+strings.ReplaceAll(status, "_", " "))
}

// listRead serves a list use case in the standard list envelope.
func listRead[T any](db *dbtx.DB, fn func(ctx context.Context, tx pgx.Tx, r *http.Request) ([]T, error)) http.HandlerFunc {
	return handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[T], error) {
		return handle.Page(fn(ctx, tx, r))
	})
}
