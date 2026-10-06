package billing

// PRD P3 billing configuration: Credit Policies (FR-POL-P3-06, label
// proposed in PRD P3 §7.6) for corporate accounts, and the Payment
// Configuration of installments and the night audit (FR-BIL-P3-07,
// FR-EOD-03, open questions #10, #13, #14). Defaults follow the assumptions
// of PRD P3 §16; every value is configurable in Settings → Club Policies.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/rules"
)

// CreditPolicy is the Credit Policies document.
type CreditPolicy struct {
	DefaultCorporateLimit    string `json:"defaultCorporateLimit" doc:"Credit limit of a corporate account without its own limit"`
	PaymentTermDays          int    `json:"paymentTermDays" doc:"Invoice due date = issue date + term"`
	ReminderDaysBefore       []int  `json:"reminderDaysBefore" doc:"Invoice reminders N days before the due date"`
	OverdueReminderEveryDays int    `json:"overdueReminderEveryDays"`
	EscalateOverdueDays      int    `json:"escalateOverdueDays" doc:"Overdue invoices older than this are escalated to the Finance Manager"`
	StatementDay             int    `json:"statementDay" doc:"Day of month the corporate statements are sent"`
	OverrideValidDays        int    `json:"overrideValidDays" doc:"Validity of an approved credit override"`
}

// DefaultCreditPolicy: term 30 days, limit Rp50 jt, statement on the 1st (PRD P3 §16 #13).
var DefaultCreditPolicy = CreditPolicy{DefaultCorporateLimit: "50000000", PaymentTermDays: 30, ReminderDaysBefore: []int{7, 1},
	OverdueReminderEveryDays: 7, EscalateOverdueDays: 30, StatementDay: 1, OverrideValidDays: 7}

// PaymentConfiguration covers installments, schedule reminders and the
// night audit.
type PaymentConfiguration struct {
	MinDownPaymentPercent      string `json:"minDownPaymentPercent" doc:"Minimum down payment of an installment plan"`
	MaxInstallments            int    `json:"maxInstallments"`
	MinAmountForTerms          string `json:"minAmountForTerms" doc:"Packages / weddings from this amount may be paid in terms"`
	ScheduleReminderDaysBefore []int  `json:"scheduleReminderDaysBefore" doc:"Payment schedule reminders N days before each due date"`
	NightAuditCutoff           string `json:"nightAuditCutoff" doc:"HH:MM local time; the automatic night audit closes the previous business day after it"`
	NightAuditAuto             bool   `json:"nightAuditAuto"`
}

// DefaultPaymentConfiguration follows PRD P3 §16 #10, #11 and #14.
var DefaultPaymentConfiguration = PaymentConfiguration{MinDownPaymentPercent: "30", MaxInstallments: 12, MinAmountForTerms: "50000000",
	ScheduleReminderDaysBefore: []int{14, 8, 1}, NightAuditCutoff: "02:00", NightAuditAuto: true}

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: "billing.credit", Category: "Credit Policies", Name: "Corporate credit & invoices",
		Description: "Default corporate credit limit, payment term, invoice reminders, overdue escalation, statement day and credit overrides",
		Default:     DefaultCreditPolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: "billing.payment_configuration", Category: "Payment Configuration", Name: "Installments & night audit",
		Description: "Installment plan limits, payment schedule reminders and the automatic night audit cut-off", Default: DefaultPaymentConfiguration})
}

// LoadCreditPolicy returns the Credit Policies version in force.
func LoadCreditPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (CreditPolicy, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, "billing.credit", property, DefaultCreditPolicy)
}

// LoadPaymentConfiguration returns the Payment Configuration in force.
func LoadPaymentConfiguration(ctx context.Context, q dbtx.Querier, property uuid.UUID) (PaymentConfiguration, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, "billing.payment_configuration", property, DefaultPaymentConfiguration)
}

// round rounds to the currency places.
func round(d decimal.Decimal, cur string) decimal.Decimal { return d.Round(places(cur)) }

// yearlyNumber issues PREFIX-YYYY-NNNNN, gap-free per property and year
// (invoices, FR-BIL-P3-04): the counter row lives in billing.sequences on
// 1 January of the year and is incremented inside the business transaction.
func yearlyNumber(ctx context.Context, tx pgx.Tx, property uuid.UUID, prefix string, year int) (string, error) {
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO billing.sequences (property_id, prefix, day, last_value) VALUES ($1, $2, make_date($3, 1, 1), 1)
		ON CONFLICT (property_id, prefix, day) DO UPDATE SET last_value = billing.sequences.last_value + 1 RETURNING last_value`,
		property, prefix, year).Scan(&n)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d-%05d", prefix, year, n), nil
}

// CurrentBusinessDate is the open business day of a property (FR-EOD-05):
// the day after the last closed day, or the local date before any audit.
func CurrentBusinessDate(ctx context.Context, q dbtx.Querier, property uuid.UUID) (time.Time, error) {
	var d time.Time
	err := q.QueryRow(ctx, `SELECT billing.current_business_date($1)`, property).Scan(&d)
	return d, err
}

// localToday is the calendar date of the property.
func localToday(ctx context.Context, q dbtx.Querier, property uuid.UUID, now time.Time) time.Time {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	l := now.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// EnsureCorporateAccount returns the corporate billing account (city
// ledger) of a corporate account, opening it — and the company's customer
// profile — on first use (FR-BIL-P3-05).
func (s *Service) EnsureCorporateAccount(ctx context.Context, tx pgx.Tx, property, corporateID uuid.UUID) (Account, error) {
	var aid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM billing.customer_accounts WHERE property_id = $1 AND corporate_account_id = $2 AND account_type = 'corporate'`,
		property, corporateID).Scan(&aid)
	if err == nil {
		return GetAccount(ctx, tx, aid)
	}
	if !dbtx.IsNoRows(err) {
		return Account{}, err
	}
	var code, name string
	var email, phone *string
	if err := tx.QueryRow(ctx, `SELECT code, name, email, phone FROM crm.corporate_accounts WHERE id = $1 AND property_id = $2`, corporateID, property).
		Scan(&code, &name, &email, &phone); err != nil {
		if dbtx.IsNoRows(err) {
			return Account{}, errs.Validation("invalid_corporate_account", "corporate account not found", errs.Field("corporateAccountId", "not_found", "not in this property"))
		}
		return Account{}, err
	}
	var cid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM crm.customers WHERE property_id = $1 AND code = $2`, property, "CORP-"+code).Scan(&cid)
	if dbtx.IsNoRows(err) {
		c, err := crm.CreateCustomer(ctx, tx, property, crm.NewCustomer{Code: "CORP-" + code, Name: name, Email: deref(email), Phone: deref(phone)})
		if err != nil {
			return Account{}, err
		}
		cid = c.ID
		if _, err := tx.Exec(ctx, `UPDATE crm.customers SET customer_type = 'corporate' WHERE id = $1`, cid); err != nil {
			return Account{}, err
		}
	} else if err != nil {
		return Account{}, err
	}
	a, err := s.EnsureAccount(ctx, tx, property, cid, "corporate", nil)
	if err != nil {
		return a, err
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.customer_accounts SET corporate_account_id = $2 WHERE id = $1`, a.ID, corporateID); err != nil {
		return a, err
	}
	return GetAccount(ctx, tx, a.ID)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// creditHeadroom is the extra limit of approved, unexpired credit overrides.
func creditHeadroom(ctx context.Context, q dbtx.Querier, accountID uuid.UUID, now time.Time) (decimal.Decimal, error) {
	var raw string
	err := q.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text FROM billing.credit_overrides WHERE account_id = $1 AND status = 'approved'
		AND expires_at > $2`, accountID, now).Scan(&raw)
	return dec(raw), err
}

// oneOf adapts handle.One to a query result: oneOf[T]("invoice")(tx.Query(…)).
func oneOf[T any](what string) func(pgx.Rows, error) (T, error) {
	return func(rows pgx.Rows, err error) (T, error) { return handle.One[T](rows, err, what) }
}
