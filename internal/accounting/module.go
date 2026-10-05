// Package accounting is Accounting (PRD P4 EP-16..EP-23, Technical Doc
// §4.1 back office layer): the general ledger with append-only journals,
// automatic posting from the domain events of every module (Tech Doc §4.3),
// accounts receivable derived from the billing invoices (contract K2),
// accounts payable from the vendor invoices, cash & bank, revenue & tax
// (e-Faktur / Coretax, service charge pool, deferred revenue), financial
// periods and closing, financial reports and the transition from the
// Accounting Export with opening balances.
//
// accounting is called by nobody but internal/app and, for the financial
// period status (contract K10), billing through PeriodStatus. It reads other
// domains only through the reporting.acc_* views and their events.
package accounting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/storage"
)

// Events published by accounting (docs/p3-p4-contracts.md).
const (
	EventJournalPosted      = "accounting.journal_posted"
	EventPeriodClosed       = "accounting.period_closed"
	EventPeriodReopened     = "accounting.period_reopened"
	EventVendorPaymentMade  = "accounting.vendor_payment_made"
	EventPostingException   = "accounting.posting_exception"
	EventPaymentRunExecuted = "accounting.payment_run_executed"
	EventTaxInvoiceUploaded = "accounting.tax_invoice_uploaded"
)

// Financial period statuses (PRD P4 §7.6).
const (
	PeriodOpen       = "open"
	PeriodSoftClosed = "soft_closed"
	PeriodClosed     = "closed"
)

// Publisher publishes domain events (outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// Module is the Accounting module.
type Module struct {
	DB        *dbtx.DB
	Events    Publisher
	Approvals *approval.Engine
	Files     *storage.Files
	Engine    *resource.Engine
	// Integrations resolves the e-Faktur (Coretax) capability (FR-REV-07).
	Integrations interface {
		Resolve(ctx context.Context, capability string) (any, string, error)
	}
}

// PeriodStatus returns the status (open, soft_closed, closed) of the
// financial period of property that contains date (contract K10). Dates
// without a period yet are open.
func PeriodStatus(ctx context.Context, q dbtx.Querier, property uuid.UUID, date time.Time) (string, error) {
	var st string
	err := q.QueryRow(ctx, `SELECT status FROM accounting.periods WHERE property_id = $1 AND year = $2 AND month = $3`,
		property, date.Year(), int(date.Month())).Scan(&st)
	if dbtx.IsNoRows(err) {
		return PeriodOpen, nil
	}
	return st, err
}

// ── helpers ───────────────────────────────────────────────────────────────

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Zero
	}
	return d
}

func decp(s *string) decimal.Decimal {
	if s == nil {
		return decimal.Zero
	}
	return dec(*s)
}

func money(d decimal.Decimal) string { return d.Round(4).String() }

func actor(ctx context.Context) uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return p.UserID
	}
	return uuid.Nil
}

func actorPtr(ctx context.Context) *uuid.UUID { return id.Ptr(actor(ctx)) }

func nz(s string) *string {
	if strings.TrimSpace(s) == "" {
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

func places(cur string) int32 {
	if cur == "IDR" || cur == "JPY" || cur == "" {
		return 0
	}
	return 2
}

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func ymd(t time.Time) string { return t.Format("2006-01-02") }

func parseDate(field, v string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return t, errs.Validation("invalid_date", field+" must be a date (YYYY-MM-DD)", errs.Field(field, "invalid_date", "YYYY-MM-DD"))
	}
	return t, nil
}

// localToday is the calendar date of a property.
func localToday(ctx context.Context, q dbtx.Querier, property uuid.UUID) time.Time {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	return dateOnly(time.Now().In(loc))
}

// localDate converts an instant to the property's calendar date.
func localDate(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) time.Time {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	return dateOnly(at.In(loc))
}

func currency(ctx context.Context, q dbtx.Querier) string {
	cur, err := org.Currency(ctx, q)
	if err != nil || cur == "" {
		return "IDR"
	}
	return cur
}

// lockProperty serialises the postings of one property (event subscribers,
// sweeps and API postings), so a source document is never posted twice.
func lockProperty(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('accounting:' || $1::text))`, property)
	return err
}

// monthStart returns the first day of the month of t.
func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// monthEnd returns the last day of the month of t.
func monthEnd(t time.Time) time.Time { return monthStart(t).AddDate(0, 1, -1) }

func errf(format string, a ...any) error { return fmt.Errorf("accounting: "+format, a...) }

// getOne adapts handle.One to a query result: getOne[T]("what")(q.Query(…)).
func getOne[T any](what string) func(pgx.Rows, error) (T, error) {
	return func(rows pgx.Rows, err error) (T, error) { return handle.One[T](rows, err, what) }
}
