package billing

// PRD P3 EP-18: billing-level cashier shifts (front desk, sport reception,
// banquet …), the business day and the night audit that closes it, and the
// Daily Revenue Report frozen with the day (FR-EOD-01..06, contract K4).
// Other modules contribute checks (open POS shifts, no-show candidates) and
// liabilities (loyalty points) through registration, so billing never
// imports them (Technical Doc §4.2).

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/provision"
)

// EventBusinessDayClosed carries the frozen summary of a business day
// (contract K4: P4 posts the daily journal from it).
const EventBusinessDayClosed = "billing.business_day_closed"

// ReopenDocumentType approves reopening a closed business day (FR-EOD-05).
var ReopenDocumentType = provision.DocumentType{Code: "business_day_reopen", Module: "billing", Name: "Reopen Business Day"}

// ── cashier shift (FR-EOD-01) ─────────────────────────────────────────────

// CashierShift is a billing-level cashier shift.
type CashierShift struct {
	ID           uuid.UUID     `json:"id" db:"id"`
	Number       string        `json:"number" db:"number"`
	Station      string        `json:"station" db:"station" enum:"front_desk,sport_reception,banquet,golf,stay_desk,other"`
	CashierID    uuid.UUID     `json:"cashierId" db:"cashier_id"`
	CashierName  string        `json:"cashierName" db:"cashier_name"`
	BusinessDate string        `json:"businessDate" db:"business_date"`
	Currency     string        `json:"currency" db:"currency"`
	OpeningFloat string        `json:"openingFloat" db:"opening_float"`
	OpenedAt     time.Time     `json:"openedAt" db:"opened_at"`
	ClosedAt     *time.Time    `json:"closedAt" db:"closed_at"`
	CountedCash  *string       `json:"countedCash" db:"counted_cash"`
	ExpectedCash *string       `json:"expectedCash" db:"expected_cash"`
	Variance     *string       `json:"variance" db:"variance"`
	CloseNote    *string       `json:"closeNote" db:"close_note"`
	Status       string        `json:"status" db:"status" enum:"open,closed"`
	Totals       []MethodTotal `json:"totals" db:"-" doc:"Payments per method (live while open)"`
	CashIn       string        `json:"cashIn" db:"-"`
	CashOut      string        `json:"cashOut" db:"-"`
	CashRefunds  string        `json:"cashRefunds" db:"-"`
	Expected     string        `json:"expected" db:"-" doc:"Opening float + cash payments − cash refunds + cash in − cash out"`
}

const shiftSelect = `SELECT id, number, station, cashier_id, cashier_name, to_char(business_date, 'YYYY-MM-DD') AS business_date, currency,
	trim_scale(opening_float)::text AS opening_float, opened_at, closed_at, trim_scale(counted_cash)::text AS counted_cash,
	trim_scale(expected_cash)::text AS expected_cash, trim_scale(variance)::text AS variance, close_note, status FROM billing.cashier_shifts`

// GetCashierShift loads a shift with its live expected cash.
func GetCashierShift(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (CashierShift, error) {
	sh, err := oneOf[CashierShift]("cashier shift")(q.Query(ctx, shiftSelect+` WHERE id = $1`, sid))
	if err != nil {
		return sh, err
	}
	rows, err := q.Query(ctx, `SELECT method_type, count(*)::int, sum(amount - refunded_amount)::text FROM billing.payments
		WHERE cashier_shift_id = $1 AND status IN ('completed', 'refunded') GROUP BY method_type ORDER BY method_type`, sid)
	if err != nil {
		return sh, err
	}
	sh.Totals = []MethodTotal{}
	cash := decimal.Zero
	for rows.Next() {
		var t MethodTotal
		var raw string
		if err := rows.Scan(&t.MethodType, &t.Count, &raw); err != nil {
			rows.Close()
			return sh, err
		}
		t.Amount = dec(raw)
		if t.MethodType == "cash" {
			cash = t.Amount
		}
		sh.Totals = append(sh.Totals, t)
	}
	rows.Close()
	var in, out, refunds string
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(amount) FILTER (WHERE kind = 'cash_in'), 0)::text, coalesce(sum(amount) FILTER (WHERE kind = 'cash_out'), 0)::text
		FROM billing.cash_movements WHERE shift_id = $1`, sid).Scan(&in, &out); err != nil {
		return sh, err
	}
	// cash refunds paid out by this cashier during the shift (the refunded
	// amount is already netted above when the payment itself is in the shift)
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(r.amount), 0)::text FROM billing.refunds r JOIN billing.payments p ON p.id = r.payment_id
		WHERE r.cashier_shift_id = $1 AND r.status = 'completed' AND p.method_type = 'cash' AND p.cashier_shift_id IS DISTINCT FROM $1`, sid).Scan(&refunds); err != nil {
		return sh, err
	}
	p := places(sh.Currency)
	sh.CashIn, sh.CashOut, sh.CashRefunds = dec(in).StringFixed(p), dec(out).StringFixed(p), dec(refunds).StringFixed(p)
	sh.Expected = dec(sh.OpeningFloat).Add(cash).Sub(dec(refunds)).Add(dec(in)).Sub(dec(out)).StringFixed(p)
	return sh, nil
}

// OpenShiftInput opens a cashier shift.
type OpenShiftInput struct {
	Station      string `json:"station" enum:"front_desk,sport_reception,banquet,golf,stay_desk,other"`
	OpeningFloat string `json:"openingFloat,omitempty"`
}

// OpenCashierShift opens a shift for the signed-in cashier (one at a time).
func (s *Service) OpenCashierShift(ctx context.Context, tx pgx.Tx, property uuid.UUID, in OpenShiftInput) (CashierShift, error) {
	p := authz.From(ctx)
	if p == nil || p.UserID == uuid.Nil {
		return CashierShift{}, errs.Unauthorized("a cashier must open the shift")
	}
	switch in.Station {
	case "front_desk", "sport_reception", "banquet", "golf", "stay_desk", "other":
	default:
		return CashierShift{}, handle.Invalid("station", "invalid", "choose the cashier station")
	}
	float, err := handle.Decimal("openingFloat", in.OpeningFloat, decimal.Zero)
	if err != nil {
		return CashierShift{}, err
	}
	if float.IsNegative() {
		return CashierShift{}, handle.Invalid("openingFloat", "invalid", "zero or more")
	}
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return CashierShift{}, err
	}
	day, err := CurrentBusinessDate(ctx, tx, property)
	if err != nil {
		return CashierShift{}, err
	}
	num, err := number(ctx, tx, property, "CSH")
	if err != nil {
		return CashierShift{}, err
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO billing.cashier_shifts (id, property_id, number, station, cashier_id, cashier_name, business_date, currency,
		opening_float, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$5,$5)`, sid, property, num, in.Station, p.UserID, p.Name, day, cur,
		float.String()); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return CashierShift{}, errs.Conflict("shift_open", "you already have an open cashier shift; close it first")
		}
		return CashierShift{}, err
	}
	sh, err := GetCashierShift(ctx, tx, sid)
	if err != nil {
		return sh, err
	}
	return sh, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "open", EntityType: "billing.cashier_shift", EntityID: sid.String(),
		EntityLabel: num + " · " + p.Name, PropertyID: &property, After: sh})
}

// CashMovementInput records cash in / out of the drawer.
type CashMovementInput struct {
	Kind   string `json:"kind" enum:"cash_in,cash_out"`
	Amount string `json:"amount"`
	Reason string `json:"reason"`
}

// AddCashMovement records cash in or out (petty cash, change top-up).
func (s *Service) AddCashMovement(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, in CashMovementInput) (CashierShift, error) {
	sh, err := lockShift(ctx, tx, sid)
	if err != nil {
		return sh, err
	}
	if sh.Status != "open" {
		return sh, errs.Conflict("shift_closed", "the shift is closed")
	}
	if in.Kind != "cash_in" && in.Kind != "cash_out" {
		return sh, handle.Invalid("kind", "invalid", "cash_in or cash_out")
	}
	amt, err := handle.Decimal("amount", in.Amount, decimal.Zero)
	if err != nil {
		return sh, err
	}
	if !amt.IsPositive() {
		return sh, handle.Invalid("amount", "invalid", "positive amount")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return sh, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.cash_movements (id, property_id, shift_id, kind, amount, reason, created_by)
		VALUES ($1,$2,$3,$4,$5::numeric,$6,$7)`, id.New(), property, sid, in.Kind, amt.String(), in.Reason, id.Ptr(actor(ctx))); err != nil {
		return sh, err
	}
	out, err := GetCashierShift(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: in.Kind, EntityType: "billing.cashier_shift", EntityID: sid.String(),
		EntityLabel: sh.Number, PropertyID: &property, Reason: in.Reason, After: map[string]any{"amount": amt.String()}})
}

func lockShift(ctx context.Context, tx pgx.Tx, sid uuid.UUID) (CashierShift, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM billing.cashier_shifts WHERE id = $1 FOR UPDATE`, sid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return CashierShift{}, errs.NotFound("cashier shift")
		}
		return CashierShift{}, err
	}
	return GetCashierShift(ctx, tx, sid)
}

// CloseShiftInput closes a shift with the counted cash.
type CloseShiftInput struct {
	CountedCash string `json:"countedCash"`
	Note        string `json:"note,omitempty"`
}

// CloseCashierShift counts the drawer and records the variance.
func (s *Service) CloseCashierShift(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, in CloseShiftInput) (CashierShift, error) {
	sh, err := lockShift(ctx, tx, sid)
	if err != nil {
		return sh, err
	}
	if sh.Status != "open" {
		return sh, errs.Conflict("shift_closed", "the shift is already closed")
	}
	counted, err := handle.Decimal("countedCash", in.CountedCash, decimal.Zero)
	if err != nil {
		return sh, err
	}
	if in.CountedCash == "" || counted.IsNegative() {
		return sh, handle.Invalid("countedCash", "required", "count the cash in the drawer")
	}
	variance := counted.Sub(dec(sh.Expected))
	if !variance.IsZero() {
		if err := handle.Required("note", in.Note); err != nil {
			return sh, errs.Validation("variance_note_required", "explain the cash variance of "+variance.String(), errs.Field("note", "required", "required with a variance"))
		}
	}
	totals, _ := json.Marshal(sh.Totals)
	if _, err := tx.Exec(ctx, `UPDATE billing.cashier_shifts SET status = 'closed', closed_at = now(), counted_cash = $2::numeric, expected_cash = $3::numeric,
		variance = $4::numeric, close_note = $5, totals = $6, updated_by = $7 WHERE id = $1`, sid, counted.String(), sh.Expected, variance.String(),
		nullStr(in.Note), totals, id.Ptr(actor(ctx))); err != nil {
		return sh, err
	}
	out, err := GetCashierShift(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "close", EntityType: "billing.cashier_shift", EntityID: sid.String(),
		EntityLabel: sh.Number, PropertyID: &property, Reason: in.Note, Before: sh, After: out})
}

// ── business day & night audit (FR-EOD-02..06) ────────────────────────────

// AuditFinding is one result of a night audit check.
type AuditFinding struct {
	Check    string   `json:"check"`
	Severity string   `json:"severity" enum:"blocking,warning,info"`
	Count    int      `json:"count"`
	Message  string   `json:"message"`
	Items    []string `json:"items,omitempty"`
}

// NightAuditCheck is a check contributed by another module (e.g. open POS
// shifts) for the business day being closed.
type NightAuditCheck func(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) ([]AuditFinding, error)

// LiabilityProvider reports liabilities owned by another module (loyalty
// points) for the business day summary.
type LiabilityProvider func(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (map[string]decimal.Decimal, error)

var (
	auditMu     sync.RWMutex
	auditChecks = map[string]NightAuditCheck{}
	liabilities = map[string]LiabilityProvider{}
)

// RegisterNightAuditCheck plugs a check into the night audit.
func (s *Service) RegisterNightAuditCheck(name string, fn NightAuditCheck) {
	auditMu.Lock()
	defer auditMu.Unlock()
	auditChecks[name] = fn
}

// RegisterLiability plugs a liability provider into the day summary.
func (s *Service) RegisterLiability(name string, fn LiabilityProvider) {
	auditMu.Lock()
	defer auditMu.Unlock()
	liabilities[name] = fn
}

// BusinessDay is the state of a business date.
type BusinessDay struct {
	BusinessDate string          `json:"businessDate" db:"business_date"`
	Status       string          `json:"status" db:"status" enum:"open,closed"`
	ClosedAt     *time.Time      `json:"closedAt" db:"closed_at"`
	ReopenedAt   *time.Time      `json:"reopenedAt" db:"reopened_at"`
	ReopenReason *string         `json:"reopenReason" db:"reopen_reason"`
	Summary      json.RawMessage `json:"summary" db:"summary"`
	Current      bool            `json:"current" db:"-" doc:"The open business date new transactions go to"`
}

// NightAuditRun is one night audit attempt.
type NightAuditRun struct {
	ID           uuid.UUID      `json:"id" db:"id"`
	BusinessDate string         `json:"businessDate" db:"business_date"`
	Mode         string         `json:"mode" db:"mode" enum:"manual,auto"`
	Status       string         `json:"status" db:"status" enum:"completed,blocked"`
	Checks       []AuditFinding `json:"checks" db:"checks"`
	Exceptions   int            `json:"exceptions" db:"exceptions"`
	Warnings     int            `json:"warnings" db:"warnings"`
	StartedAt    time.Time      `json:"startedAt" db:"started_at"`
	FinishedAt   *time.Time     `json:"finishedAt" db:"finished_at"`
}

// RevenueRow is the revenue of one business line and component.
type RevenueRow struct {
	BusinessLine     string `json:"businessLine" db:"business_line"`
	RevenueComponent string `json:"revenueComponent" db:"revenue_component"`
	Liability        bool   `json:"liability" db:"liability"`
	Net              string `json:"net" db:"net"`
	Service          string `json:"service" db:"service"`
	Tax              string `json:"tax" db:"tax"`
	Total            string `json:"total" db:"total"`
}

// PaymentRow is the payments of one method and purpose.
type PaymentRow struct {
	MethodType string `json:"methodType" db:"method_type"`
	Purpose    string `json:"purpose" db:"purpose"`
	Count      int    `json:"count" db:"count"`
	Amount     string `json:"amount" db:"amount"`
}

// DailyRevenue is the Daily Revenue Report of a business day (FR-EOD-04).
type DailyRevenue struct {
	BusinessDate string            `json:"businessDate"`
	Status       string            `json:"status" enum:"open,closed"`
	Frozen       bool              `json:"frozen" doc:"Closed by the night audit; figures no longer change"`
	Revenue      []RevenueRow      `json:"revenue"`
	Charges      string            `json:"charges"`
	Net          string            `json:"net"`
	Service      string            `json:"service"`
	Tax          string            `json:"tax"`
	Payments     []PaymentRow      `json:"payments"`
	PaymentTotal string            `json:"paymentTotal"`
	Refunds      string            `json:"refunds"`
	ShiftTotal   string            `json:"shiftTotal" doc:"Payments taken in cashier and POS shifts"`
	Liabilities  map[string]string `json:"liabilities" doc:"Voucher, prepaid, annual fee, package, deposits held, loyalty points at the end of the day"`
	Invoices     int               `json:"invoicesIssued"`
	InvoiceTotal string            `json:"invoiceTotal"`
	GeneratedAt  time.Time         `json:"generatedAt"`
}

// BuildDailyRevenue computes the figures of a business day from the
// business-dated charges and payments.
func BuildDailyRevenue(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (DailyRevenue, error) {
	cur, _ := org.Currency(ctx, q)
	p := places(cur)
	d := DailyRevenue{BusinessDate: day.Format("2006-01-02"), Status: "open", GeneratedAt: clock.Now(), Liabilities: map[string]string{}}
	var err error
	if d.Revenue, err = handle.List[RevenueRow](q.Query(ctx, `SELECT l.business_line, coalesce(l.revenue_component, l.charge_type) AS revenue_component,
		l.liability, trim_scale(sum(l.net_amount))::text AS net, trim_scale(sum(l.service_amount))::text AS service, trim_scale(sum(l.tax_amount))::text AS tax,
		trim_scale(sum(l.total))::text AS total FROM billing.folio_lines l WHERE l.property_id = $1 AND l.business_date = $2::date AND l.voided_at IS NULL
		GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`, property, d.BusinessDate)); err != nil {
		return d, err
	}
	charges, net, svc, tax := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	for _, r := range d.Revenue {
		charges, net, svc, tax = charges.Add(dec(r.Total)), net.Add(dec(r.Net)), svc.Add(dec(r.Service)), tax.Add(dec(r.Tax))
	}
	d.Charges, d.Net, d.Service, d.Tax = charges.StringFixed(p), net.StringFixed(p), svc.StringFixed(p), tax.StringFixed(p)
	if d.Payments, err = handle.List[PaymentRow](q.Query(ctx, `SELECT method_type, purpose, count(*)::int AS count, trim_scale(sum(amount))::text AS amount
		FROM billing.payments WHERE property_id = $1 AND business_date = $2::date AND status IN ('completed', 'refunded') GROUP BY 1, 2 ORDER BY 1, 2`,
		property, d.BusinessDate)); err != nil {
		return d, err
	}
	pt := decimal.Zero
	for _, r := range d.Payments {
		pt = pt.Add(dec(r.Amount))
	}
	d.PaymentTotal = pt.StringFixed(p)
	var refunds, shifts, invoiceTotal string
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text FROM billing.refunds WHERE property_id = $1 AND business_date = $2::date
		AND status = 'completed'`, property, d.BusinessDate).Scan(&refunds); err != nil {
		return d, err
	}
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text FROM billing.payments WHERE property_id = $1 AND business_date = $2::date
		AND status IN ('completed', 'refunded') AND (cashier_shift_id IS NOT NULL OR shift_id IS NOT NULL)`, property, d.BusinessDate).Scan(&shifts); err != nil {
		return d, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*), coalesce(sum(total), 0)::text FROM billing.invoices WHERE property_id = $1 AND issue_date = $2::date
		AND status <> 'void'`, property, d.BusinessDate).Scan(&d.Invoices, &invoiceTotal); err != nil {
		return d, err
	}
	d.Refunds, d.ShiftTotal, d.InvoiceTotal = dec(refunds).StringFixed(p), dec(shifts).StringFixed(p), dec(invoiceTotal).StringFixed(p)
	// liabilities at the end of the business day
	end := time.Now()
	for _, lt := range []string{"voucher", "prepaid", "annual_fee", "package"} {
		v, err := Liability(ctx, q, property, lt, end)
		if err != nil {
			return d, err
		}
		d.Liabilities[lt] = v.StringFixed(p)
	}
	var held string
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(amount - applied_amount), 0)::text FROM billing.deposits WHERE property_id = $1 AND status = 'held'`,
		property).Scan(&held); err != nil {
		return d, err
	}
	d.Liabilities["deposits"] = dec(held).StringFixed(p)
	auditMu.RLock()
	providers := make(map[string]LiabilityProvider, len(liabilities))
	for k, v := range liabilities {
		providers[k] = v
	}
	auditMu.RUnlock()
	for _, fn := range providers {
		m, err := fn(ctx, q, property, end)
		if err != nil {
			return d, err
		}
		for k, v := range m {
			d.Liabilities[k] = v.StringFixed(p)
		}
	}
	return d, nil
}

// DailyRevenueOf returns the frozen summary of a closed day or the live
// figures of an open one.
func DailyRevenueOf(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (DailyRevenue, error) {
	var raw []byte
	var status string
	err := q.QueryRow(ctx, `SELECT status, summary FROM billing.business_days WHERE property_id = $1 AND business_date = $2::date`, property,
		day.Format("2006-01-02")).Scan(&status, &raw)
	if err == nil && status == "closed" && len(raw) > 2 {
		var d DailyRevenue
		if err := json.Unmarshal(raw, &d); err == nil {
			d.Status, d.Frozen = "closed", true
			return d, nil
		}
	}
	if err != nil && !dbtx.IsNoRows(err) {
		return DailyRevenue{}, err
	}
	return BuildDailyRevenue(ctx, q, property, day)
}

func (s *Service) runChecks(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) ([]AuditFinding, error) {
	ds := day.Format("2006-01-02")
	var out []AuditFinding
	list := func(sql string, args ...any) ([]string, error) {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[string])
	}
	shifts, err := list(`SELECT number || ' · ' || cashier_name FROM billing.cashier_shifts WHERE property_id = $1 AND status = 'open'
		AND business_date <= $2::date ORDER BY number`, property, ds)
	if err != nil {
		return nil, err
	}
	out = append(out, AuditFinding{Check: "open_cashier_shifts", Severity: sev(len(shifts) > 0, "blocking"), Count: len(shifts),
		Message: "Cashier shifts must be closed before the night audit", Items: shifts})
	pending, err := list(`SELECT number FROM billing.payments WHERE property_id = $1 AND status = 'pending' AND created_at < now() ORDER BY number LIMIT 50`, property)
	if err != nil {
		return nil, err
	}
	out = append(out, AuditFinding{Check: "pending_online_payments", Severity: sev(len(pending) > 0, "warning"), Count: len(pending),
		Message: "Online payments still waiting for the gateway", Items: pending})
	folios, err := list(`SELECT f.number || ' · ' || f.holder_name FROM billing.folios f WHERE f.property_id = $1 AND f.status = 'open'
		AND EXISTS (SELECT 1 FROM billing.folio_lines l WHERE l.folio_id = f.id AND l.business_date <= $2::date AND l.voided_at IS NULL)
		AND coalesce((SELECT sum(total) FROM billing.folio_lines l WHERE l.folio_id = f.id AND l.voided_at IS NULL), 0)
		  > coalesce((SELECT sum(amount - refunded_amount) FROM billing.payments p WHERE p.folio_id = f.id AND p.status IN ('completed', 'refunded')
		    AND p.purpose = 'settlement'), 0) + coalesce((SELECT sum(applied_amount) FROM billing.deposits d WHERE d.folio_id = f.id), 0)
		ORDER BY f.number LIMIT 100`, property, ds)
	if err != nil {
		return nil, err
	}
	out = append(out, AuditFinding{Check: "open_folios_with_balance", Severity: sev(len(folios) > 0, "warning"), Count: len(folios),
		Message: "Open folios with a balance (settle, charge to account or carry over)", Items: folios})
	refunds, err := list(`SELECT number FROM billing.refunds WHERE property_id = $1 AND status = 'pending' ORDER BY number`, property)
	if err != nil {
		return nil, err
	}
	out = append(out, AuditFinding{Check: "pending_refunds", Severity: sev(len(refunds) > 0, "warning"), Count: len(refunds),
		Message: "Refunds waiting for approval", Items: refunds})
	auditMu.RLock()
	names := make([]string, 0, len(auditChecks))
	for k := range auditChecks {
		names = append(names, k)
	}
	auditMu.RUnlock()
	sort.Strings(names)
	for _, n := range names {
		auditMu.RLock()
		fn := auditChecks[n]
		auditMu.RUnlock()
		f, err := fn(ctx, tx, property, day)
		if err != nil {
			return nil, fmt.Errorf("night audit check %s: %w", n, err)
		}
		out = append(out, f...)
	}
	return out, nil
}

func sev(hit bool, s string) string {
	if hit {
		return s
	}
	return "info"
}

// RunNightAudit closes the current business day (FR-EOD-03): it runs the
// checks, stops on a blocking exception, otherwise freezes the Daily
// Revenue Report, closes the day and publishes billing.business_day_closed.
func (h *HTTP) RunNightAudit(ctx context.Context, tx pgx.Tx, property uuid.UUID, mode string) (NightAuditRun, error) {
	s := h.Svc
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('night_audit:' || $1::text))`, property); err != nil {
		return NightAuditRun{}, err
	}
	day, err := CurrentBusinessDate(ctx, tx, property)
	if err != nil {
		return NightAuditRun{}, err
	}
	checks, err := s.runChecks(ctx, tx, property, day)
	if err != nil {
		return NightAuditRun{}, err
	}
	exceptions, warnings := 0, 0
	for _, c := range checks {
		switch c.Severity {
		case "blocking":
			exceptions++
		case "warning":
			warnings++
		}
	}
	rid := id.New()
	status := "completed"
	if exceptions > 0 {
		status = "blocked"
	}
	raw, _ := json.Marshal(checks)
	var by *uuid.UUID
	if u := actor(ctx); u != uuid.Nil {
		by = &u
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.night_audit_runs (id, property_id, business_date, mode, status, checks, exceptions, warnings, finished_at, run_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now(),$9)`, rid, property, day, mode, status, raw, exceptions, warnings, by); err != nil {
		return NightAuditRun{}, err
	}
	run := NightAuditRun{ID: rid, BusinessDate: day.Format("2006-01-02"), Mode: mode, Status: status, Checks: checks, Exceptions: exceptions,
		Warnings: warnings, StartedAt: clock.Now()}
	if status == "blocked" {
		if h.Notify != nil {
			users, err := notify.Holders(ctx, tx, property, "billing.night_audit.run")
			if err != nil {
				return run, err
			}
			if len(users) > 0 {
				if err := h.Notify.Send(ctx, tx, notify.Message{Event: "billing.night_audit_blocked", Category: "billing", UserIDs: users, PropertyID: &property,
					Data: map[string]any{"businessDate": run.BusinessDate, "exceptions": exceptions}, Link: "/billing/night-audit"}); err != nil {
					return run, err
				}
			}
		}
		return run, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "night_audit_blocked", EntityType: "billing.business_day",
			EntityID: run.BusinessDate, EntityLabel: "Business day " + run.BusinessDate, PropertyID: &property, After: run})
	}
	summary, err := BuildDailyRevenue(ctx, tx, property, day)
	if err != nil {
		return run, err
	}
	summary.Status, summary.Frozen = "closed", true
	sraw, _ := json.Marshal(summary)
	if _, err := tx.Exec(ctx, `INSERT INTO billing.business_days (property_id, business_date, status, closed_at, closed_by, run_id, summary)
		VALUES ($1,$2,'closed',now(),$3,$4,$5) ON CONFLICT (property_id, business_date) DO UPDATE SET status = 'closed', closed_at = now(),
		closed_by = $3, run_id = $4, summary = $5`, property, day, by, rid, sraw); err != nil {
		return run, err
	}
	if _, err := s.Events.Publish(ctx, tx, EventBusinessDayClosed, "billing.business_day", &rid, &property, summary); err != nil {
		return run, err
	}
	return run, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "night_audit", EntityType: "billing.business_day", EntityID: run.BusinessDate,
		EntityLabel: "Business day " + run.BusinessDate, PropertyID: &property, After: map[string]any{"run": run, "charges": summary.Charges,
			"payments": summary.PaymentTotal}})
}

// ReopenInput asks to reopen a closed business day.
type ReopenInput struct {
	Reason string `json:"reason"`
}

// RequestReopen submits the reopening of a closed day for approval.
func (h *HTTP) RequestReopen(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time, in ReopenInput) (BusinessDay, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return BusinessDay{}, err
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM billing.business_days WHERE property_id = $1 AND business_date = $2 FOR UPDATE`, property, day).
		Scan(&status); err != nil {
		if dbtx.IsNoRows(err) {
			return BusinessDay{}, errs.NotFound("business day")
		}
		return BusinessDay{}, err
	}
	if status != "closed" {
		return BusinessDay{}, errs.Conflict("not_closed", "the business day is not closed")
	}
	ds := day.Format("2006-01-02")
	docID := id.New()
	if _, err := tx.Exec(ctx, `UPDATE billing.business_days SET reopen_reason = $3, reopen_request_id = $4 WHERE property_id = $1 AND business_date = $2`,
		property, day, in.Reason, docID); err != nil {
		return BusinessDay{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "reopen_requested", EntityType: "billing.business_day", EntityID: ds,
		EntityLabel: "Business day " + ds, PropertyID: &property, Reason: in.Reason}); err != nil {
		return BusinessDay{}, err
	}
	if _, _, err := h.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: ReopenDocumentType.Code, DocumentID: docID, DocumentRef: ds,
		Title: "Reopen business day " + ds, PropertyID: property, Attributes: map[string]any{"businessDate": ds}}); err != nil {
		return BusinessDay{}, err
	}
	return getBusinessDay(ctx, tx, property, day)
}

// ReopenDecision reopens the day when approved.
func (h *HTTP) ReopenDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	if d.Status != approval.StatusApproved {
		return nil
	}
	var ds string
	if err := tx.QueryRow(ctx, `UPDATE billing.business_days SET status = 'open', reopened_at = now(), reopened_by = $2 WHERE reopen_request_id = $1
		AND status = 'closed' RETURNING to_char(business_date, 'YYYY-MM-DD')`, d.DocumentID, id.Ptr(d.DecidedBy)).Scan(&ds); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "reopen", EntityType: "billing.business_day", EntityID: ds,
		EntityLabel: "Business day " + ds, PropertyID: &d.PropertyID, Reason: d.Reason})
}

func getBusinessDay(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (BusinessDay, error) {
	bd, err := oneOf[BusinessDay]("business day")(q.Query(ctx, `SELECT to_char(business_date, 'YYYY-MM-DD') AS business_date, status, closed_at, reopened_at, reopen_reason,
		summary FROM billing.business_days WHERE property_id = $1 AND business_date = $2`, property, day))
	if err != nil {
		return bd, err
	}
	cur, err := CurrentBusinessDate(ctx, q, property)
	bd.Current = err == nil && cur.Format("2006-01-02") == bd.BusinessDate
	return bd, err
}

// AutoNightAudit runs the automatic night audit of a property once the
// local time passed the cut-off after the business date (PRD P3 §16 #14).
func (h *HTTP) AutoNightAudit(ctx context.Context, tx pgx.Tx, property uuid.UUID, now time.Time) (*NightAuditRun, error) {
	cfg, _, err := LoadPaymentConfiguration(ctx, tx, property)
	if err != nil || !cfg.NightAuditAuto {
		return nil, err
	}
	day, err := CurrentBusinessDate(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	loc, err := org.Location(ctx, tx, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	cut, err := time.Parse("15:04", cfg.NightAuditCutoff)
	if err != nil {
		cut, _ = time.Parse("15:04", "02:00")
	}
	due := time.Date(day.Year(), day.Month(), day.Day()+1, cut.Hour(), cut.Minute(), 0, 0, loc)
	if now.Before(due) {
		return nil, nil
	}
	var recent bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing.night_audit_runs WHERE property_id = $1 AND business_date = $2 AND status = 'blocked'
		AND started_at > now() - interval '1 hour')`, property, day).Scan(&recent); err != nil || recent {
		return nil, err
	}
	run, err := h.RunNightAudit(ctx, tx, property, "auto")
	return &run, err
}
